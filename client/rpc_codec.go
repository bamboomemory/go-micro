package client

import (
	"bytes"
	errs "errors"
	"fmt"

	"github.com/micro/go-micro/codec"
	raw "github.com/micro/go-micro/codec/bytes"
	"github.com/micro/go-micro/codec/json"
	"github.com/micro/go-micro/codec/jsonrpc"
	"github.com/micro/go-micro/codec/proto"
	"github.com/micro/go-micro/codec/protorpc"
	"github.com/micro/go-micro/errors"
	"github.com/micro/go-micro/transport"
)

const (
	lastStreamResponseError = "EOS"
)

// serverError represents an error that has been returned from
// the remote side of the RPC connection.
type serverError string

func (e serverError) Error() string {
	return string(e)
}

// errShutdown holds the specific error for closing/closed connections
var (
	errShutdown = errs.New("connection is shut down")
)

type rpcCodec struct {
	client transport.Client
	codec  codec.Codec

	req *transport.Message
	buf *readWriteCloser
}

type readWriteCloser struct {
	wbuf *bytes.Buffer
	rbuf *bytes.Buffer
}

type clientCodec interface {
	Write(*request, interface{}) error
	Read(*response, interface{}) error
	Close() error
}

type request struct {
	Service       string
	ServiceMethod string   // format: "Service.Method"
	Seq           string   // sequence number chosen by client
	next          *request // for free list in Server
}

type response struct {
	ServiceMethod string    // echoes that of the Request
	Seq           string    // echoes that of the request
	Error         string    // error, if any.
	next          *response // for free list in Server
}

var (
	DefaultContentType = "application/protobuf"

	DefaultCodecs = map[string]codec.NewCodec{
		"application/protobuf":     proto.NewCodec,
		"application/json":         json.NewCodec,
		"application/json-rpc":     jsonrpc.NewCodec,
		"application/proto-rpc":    protorpc.NewCodec,
		"application/octet-stream": raw.NewCodec,
	}
)

func (rwc *readWriteCloser) Read(p []byte) (n int, err error) {
	return rwc.rbuf.Read(p)
}

func (rwc *readWriteCloser) Write(p []byte) (n int, err error) {
	return rwc.wbuf.Write(p)
}

func (rwc *readWriteCloser) Close() error {
	rwc.rbuf.Reset()
	rwc.wbuf.Reset()
	return nil
}

func newRpcCodec(req *transport.Message, client transport.Client, c codec.NewCodec) *rpcCodec {
	rwc := &readWriteCloser{
		wbuf: bytes.NewBuffer(nil),
		rbuf: bytes.NewBuffer(nil),
	}
	r := &rpcCodec{
		buf:    rwc,
		client: client,
		codec:  c(rwc),
		req:    req,
	}
	return r
}

func (c *rpcCodec) Write(req *request, body interface{}) error {
	c.buf.wbuf.Reset()

	m := &codec.Message{
		Id:     req.Seq,
		Target: req.Service,
		Method: req.ServiceMethod,
		Type:   codec.Request,
		Header: map[string]string{
			"X-Micro-Id":      fmt.Sprintf("%v", req.Seq),
			"X-Micro-Service": req.Service,
			"X-Micro-Method":  req.ServiceMethod,
		},
	}
	if err := c.codec.Write(m, body); err != nil {
		return errors.InternalServerError("go.micro.client.codec", err.Error())
	}
	c.req.Body = c.buf.wbuf.Bytes()
	for k, v := range m.Header {
		c.req.Header[k] = v
	}
	if err := c.client.Send(c.req); err != nil {
		return errors.InternalServerError("go.micro.client.transport", err.Error())
	}
	return nil
}

func (c *rpcCodec) Read(r *response, b interface{}) error {
	var m transport.Message
	if err := c.client.Recv(&m); err != nil {
		return errors.InternalServerError("go.micro.client.transport", err.Error())
	}
	c.buf.rbuf.Reset()
	c.buf.rbuf.Write(m.Body)

	var me codec.Message
	// set headers
	me.Header = m.Header

	// read header
	err := c.codec.ReadHeader(&me, codec.Response)
	r.ServiceMethod = me.Method
	r.Seq = me.Id
	r.Error = me.Error

	// check error in header
	if len(me.Error) == 0 {
		r.Error = me.Header["X-Micro-Error"]
	}

	// check method in header
	if len(me.Method) == 0 {
		r.ServiceMethod = me.Header["X-Micro-Method"]
	}

	if len(me.Id) == 0 {
		r.Seq = me.Header["X-Micro-Id"]
	}

	if err != nil {
		return errors.InternalServerError("go.micro.client.codec", err.Error())
	}

	// read body
	if err := c.codec.ReadBody(b); err != nil {
		return errors.InternalServerError("go.micro.client.codec", err.Error())
	}

	return nil
}

func (c *rpcCodec) Close() error {
	c.buf.Close()
	c.codec.Close()
	if err := c.client.Close(); err != nil {
		return errors.InternalServerError("go.micro.client.transport", err.Error())
	}
	return nil
}
