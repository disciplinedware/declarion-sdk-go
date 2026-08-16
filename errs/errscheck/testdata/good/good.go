package good

import (
	"errors"

	"github.com/disciplinedware/declarion-sdk-go/errs"
)

func bare() error { return errs.New("entity.stale_object") }

func withMembers() error {
	return errs.New("entity.stale_object", errs.Args{"row_version": 7, "stored_row_version": 9})
}

func withCause(err error) error {
	return errs.New("llm-connector.upstream_error", errs.Args{"upstream_status": 401}).Because(err)
}

func notOurNew() error { return errors.New("boom") }
