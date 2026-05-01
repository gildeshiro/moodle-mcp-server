package tools

import (
	"reflect"
	"testing"
)

// TestExtractAnswerFieldNames covers the regex used to pull Moodle quiz
// answer-input names out of question HTML. These shapes are taken from
// real mod_quiz_get_attempt_data responses across the supported types.
func TestExtractAnswerFieldNames(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "multichoice single answer",
			html: `<div><input type="radio" name="q1234:1_answer" value="0" />A` +
				`<input type="radio" name="q1234:1_answer" value="1" />B` +
				`<input type="hidden" name="q1234:1_:flagged" value="0"/></div>`,
			// dedup — the single-answer multichoice radios share the same name.
			want: []string{"q1234:1_answer"},
		},
		{
			name: "multichoice multi (per-choice fields)",
			html: `<input type="checkbox" name="q9876:1_answer0" value="1" />` +
				`<input type="checkbox" name="q9876:1_answer1" value="1" />` +
				`<input type="checkbox" name="q9876:1_answer2" value="1" />`,
			want: []string{"q9876:1_answer0", "q9876:1_answer1", "q9876:1_answer2"},
		},
		{
			name: "shortanswer",
			html: `<input type="text" name='q42:1_answer' value="" maxlength="255"/>`,
			want: []string{"q42:1_answer"},
		},
		{
			name: "essay (text + format)",
			html: `<textarea name="q777:1_answer"></textarea>` +
				`<input type="hidden" name="q777:1_answerformat" value="1"/>`,
			want: []string{"q777:1_answer", "q777:1_answerformat"},
		},
		{
			name: "no answer fields (empty / unsupported type)",
			html: `<p>This is a description without an input.</p>`,
			want: nil,
		},
		{
			name: "ignores non-_answer inputs",
			html: `<input type="hidden" name="q1:1_:sequencecheck" value="1"/>` +
				`<input type="text" name="q1:1_answer" value=""/>`,
			want: []string{"q1:1_answer"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAnswerFieldNames(tc.html)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractAnswerFieldNames mismatch\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}
