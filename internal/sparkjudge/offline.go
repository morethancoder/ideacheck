package sparkjudge

import (
	"context"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
)

// OfflineWriter stands in for the writer on a server running the mock
// backend (make api), so the Lab routes answer without a model: every field
// it is asked for comes back saying no model wrote it. Never paired with a
// real writer.
type OfflineWriter struct{ *mock.Judge }

// Extract fills every field with a note that nothing wrote it.
func (OfflineWriter) Extract(_ context.Context, _, _ string, fields []string) (judge.Extraction, error) {
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		values[f] = "(" + f + " written offline: the mock backend writes no text)"
	}
	return judge.Extraction{Values: values, Model: mock.Name}, nil
}
