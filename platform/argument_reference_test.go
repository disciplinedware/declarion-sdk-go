package platform

import (
	"strings"
	"testing"
	"time"

	"github.com/disciplinedware/declarion-sdk-go/fileref"
)

func TestArgsReferenceValidate(t *testing.T) {
	tenant := "9d29d947-f627-4c4e-bc3f-9238574a7c89"
	key, err := fileref.NewKey("backend", tenant, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	base := ArgsReference{EntityCode: "record", FieldCode: "body", Key: key.String(), SizeBytes: 4, JSONPointer: "/actions/0/params"}
	if err := base.Validate(tenant, 4096); err != nil {
		t.Fatalf("valid reference: %v", err)
	}
	for name, reference := range map[string]ArgsReference{
		"empty size":    func() ArgsReference { r := base; r.SizeBytes = 0; return r }(),
		"too large":     func() ArgsReference { r := base; r.SizeBytes = 4097; return r }(),
		"malformed key": func() ArgsReference { r := base; r.Key = "not-a-key"; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := reference.Validate(tenant, 4096); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	foreignTenant := "a6ca169f-71fd-48bb-b604-b054a7ce8944"
	if err := base.Validate(foreignTenant, 4096); err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("foreign tenant error = %v", err)
	}
}
