export const meta = {
  apiVersion: 1,
  key: "xai",
  name: "xAI Grok Video",
  icon: "openai",
  description: {
    en: "xAI Grok video generation (text-to-video and image-to-video)",
    zh: "xAI Grok 视频生成（文生视频、图生视频）",
  },
  version: "1.0.0",
  author: { name: "unorouter" },
  channelTypes: [48],
  models: ["grok-imagine-video", "grok-video-3", "grok-video-3-10s"],
  fetchMode: "per_task",
  usageSchema: {
    size: {
      enum: ["720P", "1080P"],
      description: { en: "Requested output video resolution.", zh: "请求的输出视频分辨率。" },
    },
  },
  usageExamples: [
    { label: "grok-video-3 720P", facts: { size: "720P" } },
    { label: "grok-video-3 1080P", facts: { size: "1080P" } },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value === undefined || value === null ? "" : value).trim();
}

function baseOf(ctx) {
  return trimmed(ctx.baseUrl).replace(/\/+$/, "");
}

function authHeaders(ctx) {
  return { Authorization: "Bearer " + trimmed(ctx.apiKey) };
}

// The upstream accepts "720P"/"1080P"; 480p is unsupported and is upscaled.
const SIZE_ALIASES = {
  "480p": "720P",
  "480P": "720P",
  "720p": "720P",
  "720P": "720P",
  "1080p": "1080P",
  "1080P": "1080P",
};

function outboundSize(req) {
  for (const key of ["quality", "resolution", "size"]) {
    const raw = req[key];
    if (typeof raw === "string" && SIZE_ALIASES[raw]) return SIZE_ALIASES[raw];
  }
  return "";
}

function submitBody(ctx) {
  const req = ctx.requestBody && typeof ctx.requestBody === "object" ? ctx.requestBody : {};
  const body = Object.assign({}, req);
  body.model = trimmed(ctx.upstreamModel) || trimmed(ctx.model);

  const size = outboundSize(req);
  if (size) body.size = size;
  // Fields the upstream rejects; size carries the resolution instead.
  delete body.quality;
  delete body.resolution;
  return body;
}

export function buildSubmitRequest(ctx) {
  return {
    url: baseOf(ctx) + "/v1/video/create",
    method: "POST",
    headers: Object.assign({ "Content-Type": "application/json", Accept: "application/json" }, authHeaders(ctx)),
    body: submitBody(ctx),
    action: ctx.action === "image_to_video" || ctx.action === "text_to_video" ? ctx.action : "text_to_video",
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  const id = trimmed(body.id);
  if (!id) throw new Error("xai: task_id is empty");
  return { taskId: id, taskData: body };
}

export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const size = outboundSize(ctx.requestBody || {});
  return { units: { size: size || "720P" } };
}

export function buildQueryRequest(ctx) {
  return {
    url: baseOf(ctx) + "/v1/videos/" + trimmed(ctx.upstreamTaskId || ctx.taskId),
    method: "GET",
    headers: Object.assign({ Accept: "application/json" }, authHeaders(ctx)),
  };
}

export function parseTaskResult(ctx, body) {
  const status = trimmed(body.status).toLowerCase();
  const result = { code: 0, taskId: trimmed(body.id) || trimmed(ctx.upstreamTaskId || ctx.taskId) };

  if (status === "pending" || status === "queued") {
    result.status = "QUEUED";
    result.progress = "0%";
  } else if (status === "completed" || status === "success" || status === "done") {
    result.status = "SUCCESS";
    result.progress = "100%";
    result.url = trimmed(body.video_url);
  } else if (status === "failed" || status === "error") {
    result.status = "FAILURE";
    result.progress = "100%";
    result.reason = trimmed(body.error) || "task failed";
  } else {
    result.status = "IN_PROGRESS";
    result.progress = "50%";
  }

  const progress = Number(body.progress);
  if (Number.isFinite(progress) && progress > 0 && progress < 100) {
    result.progress = Math.trunc(progress) + "%";
  }
  return result;
}

function artifactURL(task) {
  const data = (task && task.data) || {};
  return trimmed(data.video_url) || trimmed(task && task.url);
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" && artifactURL(task) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = artifactURL(ctx);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export function extractUsageOnComplete(_task, _taskResult, _body) {
  return null;
}

function videoRequestBody(ctx, req) {
  const model = trimmed(ctx.upstreamModel) || trimmed(ctx.model) || trimmed(req.model);
  if (!model) throw new Error("model is required");
  const prompt = trimmed(req.prompt);
  const image = trimmed(req.image) || trimmed(req.input_reference);
  if (!prompt && !image) throw new Error("prompt or image is required");
  const body = { model: model, prompt: prompt };
  if (image) body.image = image;
  for (const key of ["quality", "resolution", "size", "aspect_ratio"]) {
    if (trimmed(req[key])) body[key] = trimmed(req[key]);
  }
  if (req.metadata && typeof req.metadata === "object" && !Array.isArray(req.metadata)) {
    body.metadata = req.metadata;
  }
  return { model: model, body: body, action: image ? "image_to_video" : "text_to_video" };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

// Shared stream renderer: emits one progress event per state change, a single
// output event on success, and an error event on failure.
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

function renderVideo(task) {
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
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const texts = [];
      const images = [];
      const input = req.input;
      if (typeof input === "string") {
        texts.push(input);
      } else if (Array.isArray(input)) {
        for (const item of input) {
          if (typeof item === "string") {
            texts.push(item);
            continue;
          }
          if (!item || typeof item !== "object" || Array.isArray(item)) continue;
          const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
          for (const part of content) {
            if (typeof part === "string") {
              texts.push(part);
              continue;
            }
            if (!part || typeof part !== "object" || Array.isArray(part)) continue;
            if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
            if (["input_image", "image_url"].includes(part.type)) {
              let image = part.image_url;
              if (image && typeof image === "object") image = image.url;
              if (trimmed(image)) images.push(trimmed(image));
            }
          }
        }
      }
      const merged = Object.assign({}, req, {
        prompt: trimmed(req.prompt) || texts.join("\n").trim(),
        image: trimmed(req.image) || images[0] || "",
      });
      const decoded = videoRequestBody(ctx, merged);
      return { kind: "submit", model: decoded.model, action: decoded.action, requestBody: decoded.body };
    },
    renderEvents: function (ctx, task, previousState) {
      return renderProgressEvents(ctx, task, previousState, responsesVideoText);
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "xai" },
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
      const decoded = videoRequestBody(ctx, req);
      return { kind: "submit", model: decoded.model, action: decoded.action, requestBody: decoded.body };
    },
    render: function (_ctx, task) {
      return renderVideo(task);
    },
  },
};
