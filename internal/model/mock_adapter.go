package model

import "context"

type MockAdapter struct {
	NameValue string
	CallFunc  func(ctx context.Context, req Request) ([]byte, error)
}

func (m MockAdapter) Name() string {
	if m.NameValue == "" {
		return "mock"
	}
	return m.NameValue
}

func (m MockAdapter) StatelessCall(ctx context.Context, req Request) ([]byte, error) {
	if m.CallFunc == nil {
		return []byte(`{"decision":"continue"}`), nil
	}
	return m.CallFunc(ctx, req)
}
