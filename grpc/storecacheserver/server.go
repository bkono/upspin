// Copyright 2016 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package storecacheserver is a caching proxy between a client and all stores.
// References are stored as files in the local file system.
package storecacheserver

import (
	"fmt"
	"path"

	gContext "golang.org/x/net/context"

	"upspin.io/bind"
	"upspin.io/errors"
	"upspin.io/grpc/auth"
	"upspin.io/log"
	"upspin.io/upspin"
	"upspin.io/upspin/proto"
)

// server implements upspin.Storeserver.
type server struct {
	ctx upspin.Context

	// The on disk cache.
	cache *storeCache

	// For session handling and the Ping GRPC method.
	auth.Server
}

// New creates a new StoreServer instance.
func New(ctx upspin.Context, cacheDir string, maxBytes int64) (proto.StoreServer, error) {
	c, err := newCache(path.Join(cacheDir, "storecache"), maxBytes)
	if err != nil {
		return nil, err
	}
	return &server{
		ctx:    ctx,
		Server: auth.NewServer(ctx, nil),
		cache:  c,
	}, nil
}

// storeFor returns a StoreServer instance bound to the user and endpoint specified in the session.
func (s *server) storeFor(ctx gContext.Context) (upspin.StoreServer, error) {
	// Validate that we have a session. If not, it's an auth error.
	session, err := s.SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	e := session.ProxiedEndpoint()
	if e.Transport == upspin.Unassigned {
		return nil, errors.Str("not yet configured")
	}
	return bind.StoreServer(s.ctx, e)
}

// endpointFor returns a StoreServer endpoint for the context.
func (s *server) endpointFor(ctx gContext.Context) (upspin.Endpoint, error) {
	var e upspin.Endpoint
	// Validate that we have a session. If not, it's an auth error.
	session, err := s.SessionFromContext(ctx)
	if err != nil {
		return e, err
	}
	e = session.ProxiedEndpoint()
	if e.Transport == upspin.Unassigned {
		return e, errors.Str("not yet configured")
	}
	return e, nil
}

// Get implements proto.StoreServer.
func (s *server) Get(ctx gContext.Context, req *proto.StoreGetRequest) (*proto.StoreGetResponse, error) {
	op := logf("Get %q", req.Reference)

	e, err := s.endpointFor(ctx)
	if err != nil {
		op.log(err)
		return &proto.StoreGetResponse{Error: errors.MarshalError(err)}, nil
	}

	data, locs, err := s.cache.get(s.ctx, upspin.Reference(req.Reference), e)
	if err != nil {
		op.log(err)
		return &proto.StoreGetResponse{Error: errors.MarshalError(err)}, nil
	}
	refdata := &upspin.Refdata{
		Reference: upspin.Reference(req.Reference),
		Volatile:  false, // TODO
		Duration:  0,     // TODO
	}
	resp := &proto.StoreGetResponse{
		Data:      data,
		Refdata:   proto.RefdataProto(refdata),
		Locations: proto.Locations(locs),
	}
	return resp, nil
}

// Put implements proto.StoreServer.
func (s *server) Put(ctx gContext.Context, req *proto.StorePutRequest) (*proto.StorePutResponse, error) {
	op := logf("Put %.30x...", req.Data)

	store, err := s.storeFor(ctx)
	if err != nil {
		op.log(err)
		return &proto.StorePutResponse{Error: errors.MarshalError(err)}, nil
	}

	ref, err := s.cache.put(req.Data, store)
	if err != nil {
		op.log(err)
		return &proto.StorePutResponse{Error: errors.MarshalError(err)}, nil
	}
	refdata := &upspin.Refdata{
		Reference: ref,
		Volatile:  false, // TODO
		Duration:  0,     // TODO
	}
	resp := &proto.StorePutResponse{
		Refdata: proto.RefdataProto(refdata),
	}
	return resp, nil
}

// Empty struct we can allocate just once.
var deleteResponse proto.StoreDeleteResponse

// Delete implements proto.StoreServer.
func (s *server) Delete(ctx gContext.Context, req *proto.StoreDeleteRequest) (*proto.StoreDeleteResponse, error) {
	op := logf("Delete %q", req.Reference)

	store, err := s.storeFor(ctx)
	if err != nil {
		op.log(err)
		return &proto.StoreDeleteResponse{Error: errors.MarshalError(err)}, nil
	}

	err = store.Delete(upspin.Reference(req.Reference))
	if err != nil {
		op.log(err)
		return &proto.StoreDeleteResponse{Error: errors.MarshalError(err)}, nil
	}
	s.cache.delete(upspin.Reference(req.Reference), store)
	return &deleteResponse, nil
}

// Endpoint implements proto.StoreServer. It returns the endpoint of the remote server and not of the cache.
func (s *server) Endpoint(ctx gContext.Context, req *proto.EndpointRequest) (*proto.EndpointResponse, error) {
	op := logf("Endpoint")

	e, err := s.endpointFor(ctx)
	if err != nil {
		op.log(err)
		return &proto.EndpointResponse{}, err
	}
	return &proto.EndpointResponse{
		Endpoint: &proto.Endpoint{
			Transport: int32(e.Transport),
			NetAddr:   string(e.NetAddr),
		},
	}, nil
}

func logf(format string, args ...interface{}) operation {
	s := fmt.Sprintf(format, args...)
	log.Debug.Print("grpc/storecacheserver: " + s)
	return operation(s)
}

type operation string

func (op operation) log(err error) {
	logf("%v failed: %v", op, err)
}
