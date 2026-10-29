package validation

import "testing"

type validationFixture struct {
	UserID string `json:"user_id" validate:"required,uuid"`
	Query  string `json:"query" validate:"required,notblank"`
	Mode   string `json:"mode" validate:"oneof=sync async stream"`
}

func TestMessagesReturnsValidatorErrorsAsIs(t *testing.T) {
	err := Struct(validationFixture{
		UserID: "invalid",
		Query:  "   ",
		Mode:   "other",
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}

	got := Messages(err)
	want := []string{
		"Key: 'validationFixture.user_id' Error:Field validation for 'user_id' failed on the 'uuid' tag",
		"Key: 'validationFixture.query' Error:Field validation for 'query' failed on the 'notblank' tag",
		"Key: 'validationFixture.mode' Error:Field validation for 'mode' failed on the 'oneof' tag",
	}
	if len(got) != len(want) {
		t.Fatalf("Messages() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Messages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDetailsReturnsStructuredValidatorErrors(t *testing.T) {
	err := Struct(validationFixture{
		UserID: "invalid",
		Query:  "   ",
		Mode:   "other",
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}

	got := Details(err)
	if len(got) != 3 {
		t.Fatalf("Details() len = %d, want 3 (%v)", len(got), got)
	}
	if got[0].Field != "user_id" || got[0].Tag != "uuid" {
		t.Fatalf("expected first detail to be user_id/uuid, got %+v", got[0])
	}
	if got[1].Field != "query" || got[1].Tag != "notblank" {
		t.Fatalf("expected second detail to be query/notblank, got %+v", got[1])
	}
	if got[2].Field != "mode" || got[2].Tag != "oneof" {
		t.Fatalf("expected third detail to be mode/oneof, got %+v", got[2])
	}
}
