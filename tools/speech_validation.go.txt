// Modified for Alice HA App: keep private reasoning/tool markup out of speech.
package llm

import (
    "errors"
    "regexp"
)

var serviceMarkup = regexp.MustCompile(`(?i)(?:</?[|｜]|<\s*/?\s*(?:[\w.-]+:)?(?:(?:tool_calls?|function_calls?|tool_responses?|tool_results?|think(?:ing)?|analysis|reasoning(?:_content)?)\b|(?:function|parameter|invoke)(?:\s*=|\s+|\s*>|$)))`)

func ValidateAnswer(text string) error {
    if serviceMarkup.MatchString(text) {
        return errors.New("LLM returned service markup instead of a final answer")
    }
    return nil
}
