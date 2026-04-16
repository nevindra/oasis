package oasis

import (
	"fmt"
	"strings"
)

const compactNoToolsPreamble = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.
- You already have all the context you need in the conversation above.
- Tool calls will be REJECTED and will waste your only turn — you will fail the task.
- Your entire response must be plain text: an <analysis> block followed by a <summary> block.

`

const compactAnalysisInstruction = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts. In your analysis:

1. Chronologically analyze each message. Identify:
   - The user's explicit requests and intents
   - Your approach to addressing them
   - Key decisions, technical concepts, code patterns
   - Specific details: file names, code snippets, function signatures, edits
   - Errors you ran into and how you fixed them
   - Specific user feedback, especially if the user told you to do something differently
2. Double-check for technical accuracy and completeness.

`

const compactSummaryIntro = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions. This summary must be thorough enough that development work can continue without losing context.

`

const compactCoreSections = `Your <summary> must include these sections, numbered exactly:

1. Primary Request and Intent:
   [Capture ALL of the user's explicit requests and intents in detail]

2. Key Technical Concepts:
   - [Every important technology, framework, or pattern discussed]

3. Files and Artifacts:
   - [file/artifact name]
     - [Why it matters]
     - [Changes made]
     - [Important verbatim snippets when applicable]

4. Errors and Fixes:
   - [Error]: [Fix, plus any user feedback about the correction]

5. Problem Solving:
   [Problems solved and ongoing troubleshooting]

6. All User Messages:
   - [Every user message that is not a tool result, verbatim or near-verbatim]
   - [This section is CRITICAL — list, do not summarize]

7. Pending Tasks:
   - [Task explicitly assigned but not yet complete]

8. Current Work:
   [Precisely what was being worked on immediately before this summary, including file names and recent snippets]

9. Optional Next Step:
   [Next step DIRECTLY in line with the user's most recent explicit request. Include verbatim quote from the most recent conversation showing where work left off. Do NOT start tangential work without confirming.]

`

const compactRecompactNote = `NOTE: The conversation already contains a prior summary message near its start. You are summarizing INCREMENTAL progress since that prior summary. Preserve the prior summary's contents by reference — do not re-summarize material already in it. Focus on NEW decisions, NEW work, NEW user intent since the prior summary.

`

const compactFocusHintTemplate = `Additional focus from user:
%s

Prioritize preservation of the aspects above. Other areas may be compressed more aggressively.

`

const compactOutputExample = `Here is the expected output structure:

<analysis>
[Your thought process, ensuring all points are covered]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

[all remaining numbered sections]
</summary>

Please provide your summary now.
`

// BuildCompactPrompt composes the full compaction prompt from the template
// constants. Extras are appended after the core 9 sections with sequential
// numbering. FocusHint (if non-empty) is injected as a preservation directive.
// isRecompact adds a note instructing the model to treat the input as
// already-partially-summarized.
func BuildCompactPrompt(extras []CompactSection, focusHint string, isRecompact bool) string {
	var b strings.Builder
	b.WriteString(compactNoToolsPreamble)
	b.WriteString(compactSummaryIntro)
	b.WriteString(compactAnalysisInstruction)
	b.WriteString(compactCoreSections)

	for i, ex := range extras {
		fmt.Fprintf(&b, "%d. %s:\n   %s\n\n", 10+i, ex.Title, ex.Instructions)
	}

	if isRecompact {
		b.WriteString(compactRecompactNote)
	}
	if trimmed := strings.TrimSpace(focusHint); trimmed != "" {
		fmt.Fprintf(&b, compactFocusHintTemplate, trimmed)
	}
	b.WriteString(compactOutputExample)
	return b.String()
}
