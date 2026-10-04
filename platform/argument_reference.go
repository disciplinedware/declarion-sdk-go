package platform

import (
	"fmt"

	"github.com/disciplinedware/declarion-sdk-go/fileref"
)

type ArgsReference struct {
	EntityCode  string `json:"entity_code"`
	FieldCode   string `json:"field_code"`
	Key         string `json:"key"`
	SizeBytes   int64  `json:"size_bytes"`
	JSONPointer string `json:"json_pointer"`
}

func (r ArgsReference) Validate(tenantID string, maxBytes int64) error {
	if r.EntityCode == "" || r.FieldCode == "" || r.Key == "" || r.SizeBytes <= 0 || r.SizeBytes > maxBytes {
		return fmt.Errorf("invalid argument reference bounds")
	}
	key, err := fileref.ParseKey(r.Key)
	if err != nil {
		return err
	}
	if key.TenantID != tenantID {
		return fmt.Errorf("argument reference tenant does not match caller")
	}
	return nil
}
