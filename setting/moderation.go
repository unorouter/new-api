package setting

// Content moderation backend (OpenAI omni-moderation). Screens generation prompts
// (text + image + video) before dispatch. Vendor-neutral: uses a dedicated
// moderation API key/base-url rather than coupling to any payment processor.
// Toggled per-surface from the payment-gateway admin tab.

var ModerationApiKey = ""
var ModerationBaseUrl = "https://api.openai.com"
var ModerationModel = "omni-moderation-latest"

// ModerationProvidersText / ModerationProvidersMedia are the comma-separated
// priority order of moderation backends to try, per surface. Each backend runs
// only if its credentials are configured (openai -> ModerationApiKey, creem ->
// CreemApiKey). The first provider returning a clean allow/deny decision wins;
// operational failures (rate-limit/timeout/bad response) fall through to the next,
// and if all fail the ModerationFailOpen policy applies.
//
// Text defaults to OpenAI only - Creem's prompt-moderation API is built for
// image/video, not chat text, so chat text is not routed to it. Image/video
// default to OpenAI (multimodal, free) then Creem as fallback.
var ModerationProvidersText = "openai"
var ModerationProvidersMedia = "openai,creem"

// ModerationCategoryThresholds maps an OpenAI moderation category to the minimum
// category_score (0-1) that blocks the request. A prompt is denied when ANY
// category's calibrated score reaches its threshold. Categories absent from this
// map fall back to ModerationDefaultThreshold. Lower = stricter.
//
// Defaults are tuned for a character-chat / interactive-fiction product on a
// strict processor (Stripe): hard-block CSAM and extreme categories at a very low
// threshold, while leaving room for legal fictional sexual/violent content so
// benign roleplay is not over-blocked. Operators can override per category in the
// admin tab.
var ModerationCategoryThresholds = `{"sexual/minors":0.2,"self-harm/instructions":0.5,"illicit/violent":0.6,"hate/threatening":0.6,"sexual":0.92,"violence":0.97,"violence/graphic":0.95,"harassment":0.97}`

// ModerationDefaultThreshold is applied to any category not listed in
// ModerationCategoryThresholds.
var ModerationDefaultThreshold = 0.8

// ModerationFailOpen controls behavior when the moderation backend is unreachable
// or rate-limited (operational failure, NOT a content denial). When true, the
// request is ALLOWED through on such failures so a backend outage / rate-limit
// does not block all legitimate traffic; a content denial still blocks. When
// false, operational failures block (fail closed / strict). Default true:
// availability over strictness, since a transient outage blocking every request
// is worse than a brief screening gap, and content denials are unaffected.
var ModerationFailOpen = true

// ModerationMaxInputChars caps how many characters of the prompt are sent to the
// moderation backend. The full prompt for a chat request includes the entire
// conversation history (tens of thousands of tokens for long roleplay sessions),
// which both wastes the backend's per-minute token budget (triggering spurious
// 429s) and is unnecessary - screening the most recent content is sufficient. The
// TAIL of the prompt is kept (the newest turn). 0 disables truncation.
var ModerationMaxInputChars = 8000
