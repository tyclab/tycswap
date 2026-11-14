package web

import (
	"testing"

	"github.com/tyclab/tycswap/internal/reporting"
)

func TestAccountRowExplainsOwnershipBlock(t *testing.T) {
	account := reporting.AccountSnapshot{Number: "2", Switchable: true, OwnershipBlockReason: "Account-2 has a running session profile"}
	row := accountRow(account)
	if row["ownershipBlockReason"] != account.OwnershipBlockReason || row["rotationEligible"] != false {
		t.Fatal("account row lost the ownership block")
	}
	account.OwnershipBlockReason = ""
	if _, present := accountRow(account)["ownershipBlockReason"]; present {
		t.Fatal("unblocked account retained an ownership warning")
	}
}
