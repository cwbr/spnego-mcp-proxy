//go:build darwin

package main

/*
#cgo LDFLAGS: -framework GSS
#include <GSS/GSS.h>
#include <stdlib.h>
#include <string.h>

static OM_uint32 bridge_import_name(OM_uint32 *minor, char *service, gss_name_t *name) {
	gss_buffer_desc buffer = { strlen(service), service };
	return gss_import_name(minor, &buffer, GSS_C_NT_HOSTBASED_SERVICE, name);
}

static OM_uint32 bridge_init_sec_context(OM_uint32 *minor, gss_ctx_id_t *ctx, gss_name_t name,
		void *in, size_t in_length, gss_buffer_desc *out) {
	gss_buffer_desc input = { in_length, in };
	return gss_init_sec_context(minor, GSS_C_NO_CREDENTIAL, ctx, name, GSS_SPNEGO_MECHANISM,
		GSS_C_MUTUAL_FLAG | GSS_C_SEQUENCE_FLAG, 0, GSS_C_NO_CHANNEL_BINDINGS,
		in_length > 0 ? &input : GSS_C_NO_BUFFER, NULL, out, NULL, NULL);
}

static int bridge_gss_error(OM_uint32 major) {
	return GSS_ERROR(major) != 0;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// gssNegotiator uses the macOS GSS framework with the Kerberos ticket of the
// logged-in user (the same credential cache that klist shows).
type gssNegotiator struct{}

func newNegotiator() (Negotiator, error) {
	return gssNegotiator{}, nil
}

func (gssNegotiator) NewContext(host string) (SecurityContext, error) {
	service := C.CString("HTTP@" + host)
	defer C.free(unsafe.Pointer(service))

	var minor C.OM_uint32
	var name C.gss_name_t
	major := C.bridge_import_name(&minor, service, &name)
	if C.bridge_gss_error(major) != 0 {
		return nil, fmt.Errorf("import service name HTTP@%s: %s", host, gssStatus(major, minor))
	}
	return &gssContext{host: host, name: name}, nil
}

type gssContext struct {
	host string
	name C.gss_name_t
	ctx  C.gss_ctx_id_t
}

func (c *gssContext) Step(serverToken []byte) ([]byte, bool, error) {
	var input unsafe.Pointer
	if len(serverToken) > 0 {
		input = C.CBytes(serverToken)
		defer C.free(input)
	}

	var minor C.OM_uint32
	var output C.gss_buffer_desc
	major := C.bridge_init_sec_context(&minor, &c.ctx, c.name, input, C.size_t(len(serverToken)), &output)
	defer C.gss_release_buffer(&minor, &output)
	if C.bridge_gss_error(major) != 0 {
		return nil, false, fmt.Errorf("Kerberos negotiation for HTTP@%s: %s", c.host, gssStatus(major, minor))
	}

	token := C.GoBytes(output.value, C.int(output.length))
	return token, major&C.GSS_S_CONTINUE_NEEDED == 0, nil
}

func (c *gssContext) Close() {
	var minor C.OM_uint32
	if c.ctx != nil {
		C.gss_delete_sec_context(&minor, &c.ctx, nil)
	}
	if c.name != nil {
		C.gss_release_name(&minor, &c.name)
	}
}

// gssStatus renders the major and minor status codes as readable text.
func gssStatus(major, minor C.OM_uint32) string {
	messages := gssStatusMessages(major, C.GSS_C_GSS_CODE)
	if minor != 0 {
		messages = append(messages, gssStatusMessages(minor, C.GSS_C_MECH_CODE)...)
	}
	return strings.Join(messages, "; ")
}

func gssStatusMessages(status C.OM_uint32, statusType C.int) []string {
	var messages []string
	var context C.OM_uint32
	for {
		var minor C.OM_uint32
		var buffer C.gss_buffer_desc
		major := C.gss_display_status(&minor, status, statusType, nil, &context, &buffer)
		if C.bridge_gss_error(major) != 0 {
			break
		}
		messages = append(messages, C.GoStringN((*C.char)(buffer.value), C.int(buffer.length)))
		C.gss_release_buffer(&minor, &buffer)
		if context == 0 {
			break
		}
	}
	return messages
}
