package embed

import (
	"context"
	"testing"
)

type fakeProvider struct {
	called []string
	out    []float32
}

func (f *fakeProvider) Embed(ctx context.Context, model, text string) ([]float32, error) {
	f.called = append(f.called, model+"|"+text)
	return f.out, nil
}

func TestEmbedder_BasicCall(t *testing.T) {
	fp := &fakeProvider{out: []float32{0.1, 0.2, 0.3}}
	e := NewEmbedder(fp, "embed-slug", 3)
	v, n, err := e.Embed(context.Background(), "Андрей likes грейпфрут")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 {
		t.Fatalf("len mismatch: %d", len(v))
	}
	if n == 0 {
		t.Fatal("norm should be non-zero")
	}
	if len(fp.called) != 1 || fp.called[0] != "embed-slug|Андрей likes грейпфрут" {
		t.Fatalf("provider not called correctly: %v", fp.called)
	}
}

func TestEmbedder_DimMismatchAborts(t *testing.T) {
	fp := &fakeProvider{out: []float32{0.1, 0.2, 0.3, 0.4}}
	e := NewEmbedder(fp, "embed-slug", 3)
	_, _, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("want error on dim mismatch")
	}
}
