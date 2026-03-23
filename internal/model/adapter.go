package model

import "context"

type Adapter interface {
	Name() string
	StatelessCall(ctx context.Context, req Request) ([]byte, error)
}
