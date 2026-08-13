package runware

// Runware speaks one shape for every modality: the request body is an ARRAY of task
// objects and the response is {"data":[...]} (or {"errors":[...]}). Image inference is
// synchronous - the result comes back on the same call - which is why this is a plain
// Adaptor rather than a TaskAdaptor.
//
// Models are addressed by AIR identifier, e.g. "civitai:257749@290640", so an arbitrary
// Civitai checkpoint is reachable without any per-model configuration.

const (
	taskTypeImageInference = "imageInference"

	outputTypeURL        = "URL"
	outputTypeBase64Data = "base64Data"

	// Runware prices per pixel and per step, so these bound what a single request can cost.
	// Settlement bills the cost Runware reports times the model's markup (the flat price is
	// only a floor), so a bigger request already bills proportionally more and the ceiling
	// is about capping a single request, not protecting margin.
	//
	// 4MP covers a hires-fix pass: a 1024 base re-diffused at 2048 measures $0.0038 upstream
	// against $0.0013 for the 1MP base, so cost grows slower than pixels and the 20x markup
	// holds at every size in that range.
	maxPixels = 2048 * 2048
	maxSteps  = 50
	// Runware bounds each side independently (128-2048, multiples of 64) and rejects
	// anything outside that with invalidWidth/invalidHeight, so a wide-but-small request
	// like 4096x256 passes the pixel budget and would still be refused.
	maxSide = 2048
	minSide = 128
)

// ImageInferenceTask is one element of the request array. Optional fields are pointers or
// omitempty so Runware applies its own per-model defaults rather than receiving a zero we
// invented: sending steps=0 or CFGScale=0 is not the same as omitting them.
type ImageInferenceTask struct {
	TaskType string `json:"taskType"`
	TaskUUID string `json:"taskUUID"`
	Model    string `json:"model"`

	PositivePrompt string `json:"positivePrompt"`
	NegativePrompt string `json:"negativePrompt,omitempty"`

	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	Steps         *int     `json:"steps,omitempty"`
	CFGScale      *float64 `json:"CFGScale,omitempty"`
	Scheduler     string   `json:"scheduler,omitempty"`
	Seed          *int64   `json:"seed,omitempty"`
	ClipSkip      *int     `json:"clipSkip,omitempty"`
	NumberResults int      `json:"numberResults,omitempty"`

	// Image-to-image / inpaint. Runware accepts a URL, a data URI or bare base64 here,
	// so browser-held bytes work without any object storage.
	SeedImage string   `json:"seedImage,omitempty"`
	MaskImage string   `json:"maskImage,omitempty"`
	Strength  *float64 `json:"strength,omitempty"`

	Lora       []LoraEntry      `json:"lora,omitempty"`
	Embeddings []EmbeddingEntry `json:"embeddings,omitempty"`

	OutputType    string `json:"outputType,omitempty"`
	OutputFormat  string `json:"outputFormat,omitempty"`
	OutputQuality *int   `json:"outputQuality,omitempty"`

	CheckNSFW   *bool `json:"checkNSFW,omitempty"`
	IncludeCost bool  `json:"includeCost,omitempty"`
}

// LoraEntry and EmbeddingEntry both address their resource by AIR, so a Civitai LoRA is
// referenced the same way a checkpoint is.
type LoraEntry struct {
	Model  string  `json:"model"`
	Weight float64 `json:"weight"`
}

type EmbeddingEntry struct {
	Model  string  `json:"model"`
	Weight float64 `json:"weight"`
}

// ImageResult is one element of the response data array.
type ImageResult struct {
	TaskType        string  `json:"taskType"`
	TaskUUID        string  `json:"taskUUID"`
	ImageUUID       string  `json:"imageUUID"`
	ImageURL        string  `json:"imageURL"`
	ImageBase64Data string  `json:"imageBase64Data"`
	ImageDataURI    string  `json:"imageDataURI"`
	NSFWContent     bool    `json:"NSFWContent"`
	Cost            float64 `json:"cost"`
	Seed            int64   `json:"seed"`
}

type Response struct {
	Data   []ImageResult   `json:"data"`
	Errors []ResponseError `json:"errors"`
}

// ResponseError is Runware's per-task error. A request can partially succeed, so errors
// arrive alongside data rather than replacing it.
type ResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Parameter string `json:"parameter"`
	TaskUUID  string `json:"taskUUID"`
}
