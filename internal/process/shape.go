package process

import (
	"ai-text-shaper/internal/openai"
	"fmt"
	"regexp"
	"strings"
)

type GenerativeAIClient interface {
	RequestCreateChatCompletion(*openai.CreateChatCompletion) (*openai.ChatCompletion, error)
	MakeCreateChatCompletion(prompt string) *openai.CreateChatCompletion
}

type ShapeResult struct {
	Prompt    string
	RawResult string
	Result    string
}

func NewShapeResult(prompt, rawResult, result string) *ShapeResult {
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return &ShapeResult{Prompt: prompt, RawResult: rawResult, Result: result}
}

type Shaper struct {
	gai                      GenerativeAIClient
	maxCompletionRepeatCount int
	useFirstCodeBlock        bool
	promptOptimize           bool
}

func NewShaper(gai GenerativeAIClient, maxCompletionRepeatCount int, useFirstCodeBlock bool, promptOptimize bool) *Shaper {
	return &Shaper{gai: gai, maxCompletionRepeatCount: maxCompletionRepeatCount, useFirstCodeBlock: useFirstCodeBlock, promptOptimize: promptOptimize}
}

func (s *Shaper) ShapeText(promptOrg, inputOrg string) (*ShapeResult, error) {
	if inputOrg == "" && !s.promptOptimize {
		rawResult, err := s.requestCreateChatCompletion(promptOrg)
		if err != nil {
			return nil, err
		}
		result := rawResult
		return NewShapeResult(promptOrg, rawResult, result), nil
	}

	optimized := optimizePrompt(promptOrg, inputOrg)
	rawResult, err := s.requestCreateChatCompletion(optimized)
	if err != nil {
		return nil, err
	}
	result, err := optimizeResponseResult(rawResult, s.useFirstCodeBlock)
	if err != nil {
		return nil, err
	}
	return NewShapeResult(optimized, rawResult, result), nil
}

func (s *Shaper) requestCreateChatCompletion(prompt string) (string, error) {
	var result string
	cr := s.gai.MakeCreateChatCompletion(prompt)

	//count := s.maxCompletionRepeatCount
	maxCount := 1
	for i := 0; i < maxCount; i++ {
		comp, err := s.gai.RequestCreateChatCompletion(cr)
		if err != nil {
			return "", fmt.Errorf("failed to send chat message: %w", err)
		}

		if comp.Choices == nil || len(comp.Choices) == 0 {
			break
		}
		// use the first choice only
		choice := comp.Choices[0]
		result += choice.Message.Content
		if choice.FinishReason != "length" {
			break
		}
		// can not continue exceed response size limit
		/*
			cr.Messages = append(cr.Messages,
				openai.ChatMessage{Role: "assistant", Content: choice.Message.Content},
				// openai.ChatMessage{Role: "system", Content: "Continue from where you left off."},
			)
		*/
	}

	return result, nil
}

func optimizePrompt(prompt, input string) string {
	supplements := []string{
		"The subject of the Instruction is the area enclosed by the ai-text-shaper-input tag.",
		"The result should be returned in the language of the Instruction, but if the Instruction has a language specification, that language should be given priority.",
		"Provide additional explanations or details only if explicitly requested in the Instruction.",
		// "Only the result shall be returned.",
	}
	supplementation := strings.Join(supplements, " ")
	mergedPrmpt := fmt.Sprintf(`<Instruction>%s. (%s)</Instruction>
<ai-text-shaper-input>%s</ai-text-shaper-input>`, prompt, supplementation, input)
	return mergedPrmpt
}

func optimizeResponseResult(rawResult string, useFirstCodeBlock bool) (string, error) {
	result := rawResult
	if strings.HasPrefix(result, "```") && strings.HasSuffix(result, "```") {
		lines := strings.Split(result, "\n")
		if len(lines) > 2 {
			result = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	if useFirstCodeBlock {
		codeBlock, err := findMarkdownFirstCodeBlock(result)
		if err != nil {
			return "", fmt.Errorf("error finding first code block: %w", err)
		}
		if codeBlock != "" {
			result = codeBlock
		}
	}
	return result, nil
}

func findMarkdownFirstCodeBlock(text string) (string, error) {
	re, err := regexp.Compile("(?s)```[a-zA-Z0-9]*?\n(.*?\n)```")
	if err != nil {
		return "", fmt.Errorf("error compiling regex: %w", err)
	}
	match := re.FindStringSubmatch(text)
	if match != nil {
		return match[1], nil
	}
	return "", nil
}
