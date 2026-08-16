package bad

import (
	sdkerrs "github.com/disciplinedware/declarion-sdk-go/errs"
)

func undeclaredCode() error { return sdkerrs.New("entity.stale_objekt") }

func oldSpelling() error { return sdkerrs.New("STALE_OBJECT") }

func undeclaredMember() error {
	return sdkerrs.New("entity.stale_object", sdkerrs.Args{"rowversion": 7})
}

func illegalMemberName() error {
	return sdkerrs.New("entity.stale_object", sdkerrs.Args{"id": 7})
}

func twoArgs() error {
	return sdkerrs.New("entity.stale_object", sdkerrs.Args{"row_version": 7}, sdkerrs.Args{"stored_row_version": 9})
}

func assembledCode(code string) error { return sdkerrs.New(code) }
