export const meta = {
  apiVersion: 1,
  key: "aihorde",
  name: "AI Horde",
  icon: "openai",
  description: {
    en: "AI Horde crowdsourced Stable Diffusion image generation",
    zh: "AI Horde 众包 Stable Diffusion 图像生成",
  },
  version: "1.0.0",
  author: { name: "unorouter" },
  channelTypes: [62],
  // The published ids this channel serves; each maps to a real Horde checkpoint
  // through the channel's workflow_templates (horde_model).
  models: [
    "deliberate:free",
    "juggernaut-xl:free",
    "albedobase-xl-31:free",
    "albedobase-xl-sdxl:free",
    "icbinp-i-cant-believe-its-not-photography:free",
    "absolutereality:free",
    "dreamshaper:free",
    "rev-animated:free",
    "cyberrealistic-pony:free",
    "nova-anime-xl:free",
    "anything-v5:free",
    "amponyxl:free",
    "prefect-pony:free",
    "nova-furry-pony:free",
    "flat-2d-animerge:free",
    "quiet-goodnight-xl:free",
    "tunix-pony:free",
    "fustercluck:free",
    "swamponyxl:free",
    "wai-nsfw-illustrious-sdxl:free",
    "wai-cute-pony:free",
    "ntr-mix-il-noob-xl:free",
  ],
  fetchMode: "per_task",
  usageSchema: {
    images: {
      type: "number",
      unit: "count",
      description: { en: "Number of images requested.", zh: "请求生成的图片数量。" },
    },
    steps: {
      type: "number",
      unit: "count",
      description: { en: "Sampler steps requested.", zh: "请求的采样步数。" },
    },
  },
  usageExamples: [
    { label: "1 image, 25 steps", facts: { images: 1, steps: 25 } },
    { label: "1 image, 30 steps", facts: { images: 1, steps: 30 } },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

// AI Horde identifies its clients by a "name:version:contact" agent string.
const CLIENT_AGENT = "unorouter:1.0:https://unorouter.com";
const DEFAULT_BASE_URL = "https://aihorde.net";

function trimmed(value) {
  return String(value === undefined || value === null ? "" : value).trim();
}

function baseOf(ctx) {
  const base = trimmed(ctx.baseUrl) || DEFAULT_BASE_URL;
  return base.replace(/\/+$/, "");
}

function headers(ctx) {
  return {
    apikey: trimmed(ctx.apiKey),
    "Client-Agent": CLIENT_AGENT,
    "Content-Type": "application/json",
    Accept: "application/json",
  };
}

// Horde only accepts dimensions on a 64px grid, clamped to 64..3072. Zero passes
// through so an unset dimension falls back to the worker default.
function roundTo64(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return 0;
  let r = Math.floor(n / 64) * 64;
  if (r < 64) r = 64;
  if (r > 3072) r = 3072;
  return r;
}

function parseSize(size) {
  const parts = trimmed(size).toLowerCase().split("x");
  if (parts.length !== 2) return [0, 0];
  const w = Number.parseInt(parts[0].trim(), 10);
  const h = Number.parseInt(parts[1].trim(), 10);
  if (!Number.isFinite(w) || !Number.isFinite(h)) return [0, 0];
  return [w, h];
}

function metaInt(source, key) {
  if (!source) return 0;
  const value = source[key];
  if (typeof value === "number" && Number.isFinite(value)) return Math.trunc(value);
  if (typeof value === "string") {
    const parsed = Number.parseInt(value.trim(), 10);
    if (Number.isFinite(parsed)) return parsed;
  }
  return 0;
}

function metaFloat(source, key) {
  if (!source) return 0;
  const value = source[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const parsed = Number.parseFloat(value.trim());
    if (Number.isFinite(parsed)) return parsed;
  }
  return 0;
}

// Per-model defaults ship with the channel as workflow_templates JSON:
// { "models": { "<published-id>": { horde_model, width, height, steps, cfg_scale, ... } } }
// The published model id is not what Horde expects on the wire, so horde_model
// carries the real checkpoint name (e.g. "deliberate:free" -> "Deliberate").
function modelDefaults(ctx, model) {
  const raw = ctx.channelConfig;
  if (!raw) return {};
  let config = raw;
  if (typeof raw === "string") {
    if (!trimmed(raw)) return {};
    try {
      config = JSON.parse(raw);
    } catch {
      return {};
    }
  }
  if (!config || typeof config !== "object") return {};
  const models = config.models;
  if (!models || typeof models !== "object") return {};
  const entry = models[model];
  return entry && typeof entry === "object" ? entry : {};
}

function buildSubmitBody(ctx) {
  const req = ctx.requestBody || {};
  const metadata = req.metadata && typeof req.metadata === "object" ? req.metadata : {};
  const model = trimmed(ctx.upstreamModel) || trimmed(ctx.model);
  const defaults = modelDefaults(ctx, model);

  const prompt = trimmed(req.prompt);
  if (!prompt) throw new Error("aihorde: prompt is required");

  const params = {
    width: roundTo64(defaults.width),
    height: roundTo64(defaults.height),
    steps: metaInt(defaults, "steps"),
    cfg_scale: metaFloat(defaults, "cfg_scale"),
    sampler_name: trimmed(defaults.sampler_name),
    n: 1,
  };
  if (typeof defaults.karras === "boolean") params.karras = defaults.karras;
  if (metaInt(defaults, "clip_skip") > 0) params.clip_skip = metaInt(defaults, "clip_skip");

  // Client-supplied size overrides the per-model default dimensions.
  const [w, h] = parseSize(req.size);
  if (w > 0 && h > 0) {
    params.width = roundTo64(w);
    params.height = roundTo64(h);
  }

  if (metaInt(metadata, "n") > 0) params.n = metaInt(metadata, "n");
  if (metaInt(metadata, "steps") > 0) params.steps = metaInt(metadata, "steps");
  if (metaFloat(metadata, "cfg_scale") > 0) params.cfg_scale = metaFloat(metadata, "cfg_scale");
  if (trimmed(metadata.sampler_name)) params.sampler_name = trimmed(metadata.sampler_name);
  const seed = trimmed(metadata.seed) || (metaInt(metadata, "seed") > 0 ? String(metaInt(metadata, "seed")) : "");
  if (seed) params.seed = seed;

  for (const key of Object.keys(params)) {
    const value = params[key];
    if (value === "" || value === 0 || value === undefined) delete params[key];
  }
  if (!params.n) params.n = 1;

  // OpenAI-style callers pass the negative prompt separately; Horde encodes it
  // inline as "prompt ### negative".
  const negative = trimmed(metadata.negative_prompt);
  const fullPrompt = negative ? prompt + " ### " + negative : prompt;

  return {
    prompt: fullPrompt,
    params: params,
    models: [trimmed(defaults.horde_model) || model],
    // The uncensored flags are what make Horde serve the NSFW/RP finetunes this
    // channel publishes without prompt sanitizing.
    nsfw: true,
    censor_nsfw: false,
    replacement_filter: false,
    r2: true,
    slow_workers: true,
  };
}

export function buildSubmitRequest(ctx) {
  return {
    url: baseOf(ctx) + "/api/v2/generate/async",
    method: "POST",
    headers: headers(ctx),
    body: buildSubmitBody(ctx),
    action: "generate",
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  // Horde's async submit answers 202 Accepted; the task pipeline only treats a
  // 2xx submit as success, so surface a real error for anything else.
  if (resp.status && resp.status >= 400) {
    throw new Error("aihorde upstream " + resp.status + ": " + JSON.stringify(body).slice(0, 400));
  }
  const id = trimmed(body.id);
  if (!id) throw new Error("aihorde: no task id in submit response");
  return { taskId: id, taskData: body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const body = buildSubmitBody(ctx);
  return { units: { images: body.params.n || 1, steps: body.params.steps || 0 } };
}

export function buildQueryRequest(ctx) {
  return {
    url: baseOf(ctx) + "/api/v2/generate/status/" + trimmed(ctx.upstreamTaskId || ctx.taskId),
    method: "GET",
    headers: { apikey: trimmed(ctx.apiKey), "Client-Agent": CLIENT_AGENT, Accept: "application/json" },
  };
}

function generationURLs(body) {
  const generations = Array.isArray(body.generations) ? body.generations : [];
  const urls = [];
  for (const generation of generations) {
    const img = trimmed(generation && generation.img);
    if (!img) continue;
    // r2:false workers return inline base64 webp instead of an R2 link.
    urls.push(img.startsWith("http://") || img.startsWith("https://") ? img : "data:image/webp;base64," + img);
  }
  return urls;
}

export function parseTaskResult(ctx, body) {
  const result = { code: 0, taskId: trimmed(ctx.upstreamTaskId || ctx.taskId) };
  if (body.faulted) {
    result.status = "FAILURE";
    result.reason = trimmed(body.message) || "aihorde: request faulted";
    return result;
  }
  if (body.is_possible === false) {
    result.status = "FAILURE";
    result.reason = trimmed(body.message) || "aihorde: no worker can fulfill this request";
    return result;
  }
  if (body.done) {
    const urls = generationURLs(body);
    if (!urls.length) {
      result.status = "FAILURE";
      result.reason = "aihorde: done but no image returned";
      return result;
    }
    result.status = "SUCCESS";
    result.progress = "100%";
    result.url = urls[0];
    if (urls.length > 1) result.urls = urls;
    return result;
  }
  if (Number(body.processing) > 0) {
    result.status = "IN_PROGRESS";
    result.progress = "50%";
    return result;
  }
  result.status = "QUEUED";
  result.progress = "0%";
  return result;
}

function artifactURL(task) {
  const data = (task && task.data) || {};
  const urls = generationURLs(data);
  if (urls.length) return urls[0];
  return trimmed(task && task.url);
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" && artifactURL(task) ? [{ key: "image", type: "image" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "image") throw new Error("artifact_not_found");
  const url = artifactURL(ctx);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(_task, _taskResult, body) {
  const urls = generationURLs(body || {});
  if (!urls.length) return null;
  return { units: { images: urls.length } };
}

function responsesImageText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.image;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("image artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<img src="' + escaped + '" alt="">';
}

function renderProgressEvents(ctx, task, previousState, textOf) {
  const status = String(task.status || "UNKNOWN").toUpperCase();
  const value = Number(String(task.progress || "").replace("%", ""));
  const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
  const state = { status: status, progress: progress };
  if (status === "SUCCESS") {
    const text = textOf(ctx);
    const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
    return { events: events, state: state, done: true };
  }
  if (status === "FAILURE") {
    return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
  }
  if (previousState && previousState.status === status && previousState.progress === progress) {
    return { events: [], state: state, done: false };
  }
  const event = { type: "progress", message: status.toLowerCase() };
  if (progress !== null) event.progress = progress;
  return { events: [event], state: state, done: false };
}

// Pulls prompt text and any image refs out of an OpenAI Responses `input`.
function responsesInput(req) {
  const texts = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") texts.push(part);
        else if (part && typeof part === "object" && ["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
      }
    }
  }
  return texts.join("\n").trim();
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(ctx.upstreamModel) || trimmed(ctx.model) || trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata))) {
        throw new Error("metadata must be an object");
      }
      const prompt = trimmed(req.prompt) || responsesInput(req);
      if (!prompt) throw new Error("input is required");
      const requestBody = { model: model, prompt: prompt };
      if (trimmed(req.size)) requestBody.size = trimmed(req.size);
      if (req.metadata) requestBody.metadata = req.metadata;
      return { kind: "submit", model: model, action: "generate", requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      return renderProgressEvents(ctx, task, previousState, responsesImageText);
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesImageText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "aihorde" },
      };
    },
  },
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) {
        throw new Error("JSON or multipart body required");
      }
      let req;
      if (ctx.body.kind === "json") {
        if (!ctx.body.value || typeof ctx.body.value !== "object" || Array.isArray(ctx.body.value)) {
          throw new Error("JSON object required");
        }
        req = Object.assign({}, ctx.body.value);
      } else {
        req = {};
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          const values = fields[name] || [];
          if (values.length > 1) throw new Error(name + " must be provided once");
          req[name] = values[0];
        }
        if (req.metadata !== undefined) {
          let parsed;
          try {
            parsed = JSON.parse(req.metadata);
          } catch {
            throw new Error("metadata must be a JSON object string");
          }
          if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
            throw new Error("metadata must be a JSON object string");
          }
          req.metadata = parsed;
        }
      }
      const model = trimmed(ctx.upstreamModel) || trimmed(ctx.model) || trimmed(req.model);
      if (!model) throw new Error("model is required");
      const prompt = trimmed(req.prompt);
      if (!prompt) throw new Error("prompt is required");
      const requestBody = { model: model, prompt: prompt };
      if (trimmed(req.size)) requestBody.size = trimmed(req.size);
      if (req.metadata && typeof req.metadata === "object") requestBody.metadata = req.metadata;
      return { kind: "submit", model: model, action: "generate", requestBody: requestBody };
    },
    // AI Horde is image generation; the video envelope is the transport the task
    // pipeline shares, so report the image task's own state through it.
    render: function (_ctx, task) {
      const statusMap = { QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
      const output = {
        id: task.task_id,
        object: "video",
        model: "",
        status: statusMap[task.status] || "unknown",
        progress: Number(String(task.progress || "0").replace("%", "")),
        created_at: task.created_at,
      };
      if (task.updated_at) output.completed_at = task.updated_at;
      if (task.fail_reason) output.error = { message: task.fail_reason, code: "task_failed" };
      return output;
    },
  },
};
