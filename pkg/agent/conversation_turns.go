// See LICENSE for licensing information

package agent

// ConversationTurn captures one prior session turn with its tool history.
type ConversationTurn struct {
	User      Message
	Assistant *Message
	Steps     []Step
}

func cloneConversationTurns(turns []ConversationTurn) []ConversationTurn {
	if len(turns) == 0 {
		return nil
	}
	cloned := make([]ConversationTurn, len(turns))
	for idx, turn := range turns {
		cloned[idx] = ConversationTurn{
			User:  turn.User,
			Steps: cloneSteps(turn.Steps),
		}
		if turn.Assistant != nil {
			assistant := *turn.Assistant
			cloned[idx].Assistant = &assistant
		}
	}
	return cloned
}

func flattenConversationMessages(turns []ConversationTurn) []Message {
	if len(turns) == 0 {
		return nil
	}
	messages := make([]Message, 0, len(turns)*2)
	for _, turn := range turns {
		if turn.User.Role != "" || turn.User.Content != "" {
			messages = append(messages, turn.User)
		}
		if turn.Assistant != nil {
			messages = append(messages, *turn.Assistant)
		}
	}
	return messages
}

func flattenConversationSteps(turns []ConversationTurn) []Step {
	if len(turns) == 0 {
		return nil
	}
	var total int
	for _, turn := range turns {
		total += len(turn.Steps)
	}
	if total == 0 {
		return nil
	}
	steps := make([]Step, 0, total)
	for _, turn := range turns {
		steps = append(steps, cloneSteps(turn.Steps)...)
	}
	return steps
}
