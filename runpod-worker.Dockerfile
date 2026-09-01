# RunPod serverless worker for the ComfyUI channel. Bakes the four production
# checkpoints + Flux 2 dev fp8 bundle into the image so cold starts read from
# local SSD instead of the network volume.
#
# Build context is the repo root; this Dockerfile only adds files under
# /comfyui/models/. None of the new-api Go code is needed in the worker.
#
# Edge case 14 (see comfyui-runpod-memory.md): worker-comfyui's
# extra_model_paths.yaml only scans `unet/` and `clip/`, NOT
# `diffusion_models/` and `text_encoders/`. Flux 2 weights MUST land in
# unet/ and clip/ respectively or ComfyUI returns value_not_in_list.

FROM runpod/worker-comfyui:5.8.5-base

ENV HF_HUB_ENABLE_HF_TRANSFER=1
RUN pip install --no-cache-dir -U "huggingface_hub[hf_transfer]"

# SDXL fine-tunes. HF_TOKEN mounted to bypass anonymous rate limits on
# 6.9 GB downloads (community mirrors are public but throttle anonymous
# requests). Token never lands in image layers (build secret).
RUN --mount=type=secret,id=hf_token,env=HF_TOKEN \
    hf download Romanos575/prefectPonyXL_v4 prefectPonyXL_v40.safetensors \
        --local-dir /comfyui/models/checkpoints/ \
    && hf download fandyy24/lustifySDXLNSFW_endgame lustifySDXLNSFW_endgame.safetensors \
        --local-dir /comfyui/models/checkpoints/ \
    && hf download stabilityai/stable-diffusion-xl-base-1.0 sd_xl_base_1.0.safetensors \
        --local-dir /comfyui/models/checkpoints/

# Flux 2 dev (Comfy-Org fp8 bundle, gated repo).
# Files live under split_files/<type>/ in the source repo; we strip that
# prefix on the local side so they land in the dirs worker-comfyui scans.
RUN --mount=type=secret,id=hf_token,env=HF_TOKEN \
    hf download Comfy-Org/flux2-dev split_files/diffusion_models/flux2_dev_fp8mixed.safetensors \
        --local-dir /tmp/hf-flux2 \
    && mv /tmp/hf-flux2/split_files/diffusion_models/flux2_dev_fp8mixed.safetensors /comfyui/models/unet/ \
    && hf download Comfy-Org/flux2-dev split_files/text_encoders/mistral_3_small_flux2_fp8.safetensors \
        --local-dir /tmp/hf-flux2 \
    && mv /tmp/hf-flux2/split_files/text_encoders/mistral_3_small_flux2_fp8.safetensors /comfyui/models/clip/ \
    && hf download Comfy-Org/flux2-dev split_files/vae/flux2-vae.safetensors \
        --local-dir /tmp/hf-flux2 \
    && mv /tmp/hf-flux2/split_files/vae/flux2-vae.safetensors /comfyui/models/vae/ \
    && rm -rf /tmp/hf-flux2
