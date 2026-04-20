package preprocess

// extractionPrompt returns the system prompt for the fast model extraction pass.
// This prompt is designed for structured output — the model must return valid
// JSON conforming to the extraction schema. No prose, no explanation.
func extractionPrompt() string {
	return `You are a fast message classifier. Extract structured metadata from the user's message.

Return ONLY valid JSON. No markdown, no explanation, no extra text.

Schema:
{
  "intent": one of ["question", "command", "status_update", "artifact_share", "conversation", "correction", "unknown"],
  "intent_confidence": float 0.0-1.0,
  "entities": array of {"name": string, "type": one of ["project", "person", "tool", "file", "session", "branch"], "confidence": float 0.0-1.0, "match_type": one of ["exact", "fuzzy", "inferred"]},
  "topics": array of ["code", "debugging", "planning", "infra", "data", "review", "meta", "general"],
  "sentiment": one of ["neutral", "positive", "frustrated", "urgent"],
  "sentiment_confidence": float 0.0-1.0,
  "summary": string max 200 chars,
  "flags": optional array of ["uncertain", "multi_intent", "ambiguous_entity", "low_confidence", "needs_review"]
}

Rules:
- Entities must have non-empty names
- Confidence scores must be honest — use flags for uncertainty
- If the message is ambiguous, set flag "uncertain" and lower intent_confidence
- If multiple intents are present, set flag "multi_intent"
- Summary must be under 200 characters
- Topics should capture the domain, not the action`
}
