package dto

// TestChannelResponse is the response for channel test endpoints.
type TestChannelResponse struct {
	Success   bool    `json:"success"`
	Message   string  `json:"message"`
	Time      float64 `json:"time"`
	ErrorCode any     `json:"error_code,omitempty"`
}

// ChannelBalanceResponse is the response for channel balance endpoints.
type ChannelBalanceResponse struct {
	Success     bool    `json:"success"`
	Message     string  `json:"message"`
	Balance     float64 `json:"balance,omitempty"`
	RawResponse string  `json:"raw_response,omitempty"`
}

// AddChannelResult lists the channels POST /api/channel created; Id is set
// when exactly one was created. Results maps each input key to its channel.
type AddChannelResult struct {
	Id      int                     `json:"id,omitempty"`
	Ids     []int                   `json:"ids"`
	Results []AddChannelInputResult `json:"results"`
}

type AddChannelInputResult struct {
	InputIndex int `json:"input_index"`
	ChannelId  int `json:"channel_id"`
}
