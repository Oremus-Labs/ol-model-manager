"""
Patch vLLM's TransformersMistralTokenizer so GPT-OSS models load their
bundled tekken.json through Hugging Face's fast tokenizer stack.
"""

from __future__ import annotations

import os
from pathlib import Path

from transformers import PreTrainedTokenizerFast

if os.environ.get("SKIP_GPTOSS_PATCH") == "1":  # pragma: no cover
    vllm_mistral = None
else:
    try:
        from vllm.transformers_utils.tokenizers import mistral as vllm_mistral
    except Exception:  # pragma: no cover - unavailable in non-vLLM contexts
        vllm_mistral = None


class GPTOSSTokenizerFast(PreTrainedTokenizerFast):
    vocab_files_names = {"tokenizer_file": "tekken.json"}
    slow_tokenizer_class = None
    model_input_names = ["input_ids", "attention_mask"]

    @classmethod
    def from_pretrained(cls, pretrained_model_name_or_path, *inputs, **kwargs):  # type: ignore[override]
        if "tokenizer_file" not in kwargs or kwargs["tokenizer_file"] is None:
            base = Path(pretrained_model_name_or_path)
            for candidate in ("tekken.json", "tokenizer.json"):
                candidate_path = base / candidate
                if candidate_path.exists():
                    kwargs["tokenizer_file"] = str(candidate_path)
                    break
        return super().from_pretrained(pretrained_model_name_or_path, *inputs, **kwargs)


_target_class = None
if vllm_mistral is not None:
    if hasattr(vllm_mistral, "TransformersMistralTokenizer"):
        _target_class = vllm_mistral.TransformersMistralTokenizer
    elif hasattr(vllm_mistral, "TransformersTokenizer"):
        _target_class = vllm_mistral.TransformersTokenizer

if _target_class is not None:

    _original_mistral_from_pretrained = _target_class.from_pretrained.__func__

    @classmethod
    def _patched_mistral_from_pretrained(cls, pretrained_model_name_or_path, *inputs, **kwargs):
        base = Path(pretrained_model_name_or_path)
        tekken_file = base / "tekken.json"
        tokenizer_json = base / "tokenizer.json"
        if tekken_file.exists() or tokenizer_json.exists():
            return GPTOSSTokenizerFast.from_pretrained(
                pretrained_model_name_or_path, *inputs, **kwargs
            )
        return _original_mistral_from_pretrained(cls, pretrained_model_name_or_path, *inputs, **kwargs)

    _target_class.from_pretrained = classmethod(_patched_mistral_from_pretrained)
