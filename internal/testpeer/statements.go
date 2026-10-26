package testpeer

import (
	"context"
	"sync"

	statementdatav1 "github.com/fin-assist/contracts/gen/go/finassist/statementdata/v1"
	"google.golang.org/grpc"
)

// Statements fakes the generated import service; unset methods remain UNIMPLEMENTED.
type Statements struct {
	statementdatav1.UnimplementedStatementsServiceServer
	mu         sync.Mutex
	ConfigFunc func(context.Context, *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error)
	ListFunc   func(context.Context, *statementdatav1.ListUploadsRequest) (*statementdatav1.ListUploadsResponse, error)
	UploadFunc func(grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error
	WatchFunc  func(*statementdatav1.WatchUploadRequest, grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error
	DeleteFunc func(context.Context, *statementdatav1.DeleteUploadRequest) (*statementdatav1.DeleteUploadResponse, error)
	UnreadFunc func(context.Context, *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error)
}

func (s *Statements) GetImportConfig(c context.Context, r *statementdatav1.GetImportConfigRequest) (*statementdatav1.GetImportConfigResponse, error) {
	if f := get(&s.mu, &s.ConfigFunc); f != nil {
		return f(c, r)
	}
	return s.UnimplementedStatementsServiceServer.GetImportConfig(c, r)
}
func (s *Statements) ListUploads(c context.Context, r *statementdatav1.ListUploadsRequest) (*statementdatav1.ListUploadsResponse, error) {
	if f := get(&s.mu, &s.ListFunc); f != nil {
		return f(c, r)
	}
	return s.UnimplementedStatementsServiceServer.ListUploads(c, r)
}
func (s *Statements) UploadStatement(stream grpc.ClientStreamingServer[statementdatav1.UploadStatementRequest, statementdatav1.UploadStatementResponse]) error {
	if f := get(&s.mu, &s.UploadFunc); f != nil {
		return f(stream)
	}
	return s.UnimplementedStatementsServiceServer.UploadStatement(stream)
}
func (s *Statements) WatchUpload(r *statementdatav1.WatchUploadRequest, stream grpc.ServerStreamingServer[statementdatav1.WatchUploadResponse]) error {
	if f := get(&s.mu, &s.WatchFunc); f != nil {
		return f(r, stream)
	}
	return s.UnimplementedStatementsServiceServer.WatchUpload(r, stream)
}
func (s *Statements) DeleteUpload(c context.Context, r *statementdatav1.DeleteUploadRequest) (*statementdatav1.DeleteUploadResponse, error) {
	if f := get(&s.mu, &s.DeleteFunc); f != nil {
		return f(c, r)
	}
	return s.UnimplementedStatementsServiceServer.DeleteUpload(c, r)
}
func (s *Statements) ListUnreadLines(c context.Context, r *statementdatav1.ListUnreadLinesRequest) (*statementdatav1.ListUnreadLinesResponse, error) {
	if f := get(&s.mu, &s.UnreadFunc); f != nil {
		return f(c, r)
	}
	return s.UnimplementedStatementsServiceServer.ListUnreadLines(c, r)
}

// Set changes service behavior under the fixture lock.
func (s *Statements) Set(fn func(*Statements)) { s.mu.Lock(); defer s.mu.Unlock(); fn(s) }
