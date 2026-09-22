package matter

import (
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/exitcode"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestMatterCreateResultPreservesLegacyErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		resultCode operation.ResultCode
		exitCode   int
		structured bool
	}{
		{
			name:       "validation",
			err:        wiperr.New("validation.invalid-title", "invalid title"),
			resultCode: operation.ResultRejected,
			exitCode:   exitcode.UserFail,
			structured: true,
		},
		{
			name:       "refusal",
			err:        wiperr.New("refusal.unknown-clone", "unknown clone"),
			resultCode: operation.ResultRefused,
			exitCode:   exitcode.Refusal,
			structured: true,
		},
		{
			name:       "structured internal",
			err:        wiperr.New("internal.store", "store failed"),
			resultCode: operation.ResultFailed,
			exitCode:   exitcode.Internal,
			structured: true,
		},
		{
			name:       "unstructured execution error",
			err:        errors.New("raw store failure"),
			resultCode: operation.ResultFailed,
			exitCode:   exitcode.Usage,
			structured: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := matterCreateFailure(test.err)
			if result.Code != test.resultCode {
				t.Fatalf("result code = %q, want %q", result.Code, test.resultCode)
			}
			if err := operation.MatterCreateV1.ValidateResult(result); err != nil {
				t.Fatalf("semantic result is invalid: %v", err)
			}

			_, gotErr := matterCreateOutput(result)
			if gotErr == nil || gotErr.Error() != test.err.Error() {
				t.Fatalf("adapter error = %v, want message %q", gotErr, test.err.Error())
			}
			var gotStructured *wiperr.Error
			isStructured := errors.As(gotErr, &gotStructured)
			if isStructured != test.structured {
				t.Fatalf("structured error = %t, want %t", isStructured, test.structured)
			}

			gotExit := exitcode.Usage
			if isStructured {
				gotExit = exitcode.FromError(gotStructured)
				wantStructured := test.err.(*wiperr.Error)
				if gotStructured.Code != wantStructured.Code {
					t.Fatalf("error code = %q, want %q", gotStructured.Code, wantStructured.Code)
				}
			}
			if gotExit != test.exitCode {
				t.Fatalf("exit code = %d, want %d", gotExit, test.exitCode)
			}
		})
	}
}
