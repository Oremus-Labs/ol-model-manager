FROM ghcr.io/oremus-labs/vllm-rocm-gptoss:0.11.2-gptoss

WORKDIR /opt/gptoss_patch
COPY sitecustomize.py ./sitecustomize.py

ENV PYTHONPATH=/opt/gptoss_patch:${PYTHONPATH} \
    FORCE_GPTOSS_TIKTOKEN=1
