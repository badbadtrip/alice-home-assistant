// Modified for Alice HA App: keep private reasoning/tool markup out of speech.
package llm

import (
    "errors"
    "regexp"
)

var serviceMarkup = regexp.MustCompile(`(?i)(?:</?[|｜]|<\s*/?\s*(?:[\w.-]+:)?(?:(?:tool_calls?|function_calls?|tool_responses?|tool_results?|think(?:ing)?|analysis|reasoning(?:_content)?)\b|(?:function|parameter|invoke)(?:\s*=|\s+|\s*>|$)))`)

// ErrDisabled marks a request that needs a model when none is configured.
var ErrDisabled = errors.New("LLM disabled: configure a provider for free-form requests")

// ErrNoAction marks a failure before any device was touched.
var ErrNoAction = errors.New("no home action was taken")

func ValidateAnswer(text string) error {
    if serviceMarkup.MatchString(text) {
        return errors.New("LLM returned service markup instead of a final answer")
    }
    return nil
}
