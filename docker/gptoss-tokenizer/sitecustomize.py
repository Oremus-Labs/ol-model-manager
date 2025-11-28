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
        from vllm.transformers_utils.tokenizer_base import TokenizerBase
    except Exception:  # pragma: no cover - unavailable outside runtime
        vllm_mistral = None
        TokenizerBase = object  # type: ignore[misc,assignment]


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


class GPTOSSTokenizerShim(TokenizerBase):  # type: ignore[misc]
    def __init__(self, tokenizer: PreTrainedTokenizerFast) -> None:
        self._tok = tokenizer
        # Ensure pad token exists for batching.
        if self._tok.pad_token is None and self._tok.eos_token is not None:
            self._tok.pad_token = self._tok.eos_token
        self._all_special_tokens = list(self._tok.all_special_tokens)
        self._all_special_ids = list(self._tok.all_special_ids or [])
        self._vocab = self._tok.get_vocab()
        self._max_token_id = max(self._vocab.values()) if self._vocab else 0

    @property
    def all_special_tokens_extended(self) -> list[str]:
        extended = getattr(self._tok, "all_special_tokens_extended", None)
        if extended:
            return list(extended)
        return self._all_special_tokens

    @property
    def all_special_tokens(self) -> list[str]:
        return self._all_special_tokens

    @property
    def all_special_ids(self) -> list[int]:
        return self._all_special_ids

    @property
    def bos_token_id(self) -> int:
        return self._tok.bos_token_id or self._tok.cls_token_id or 0

    @property
    def eos_token_id(self) -> int:
        return self._tok.eos_token_id or self._tok.sep_token_id or self._tok.pad_token_id or 0

    @property
    def sep_token(self) -> str:
        return self._tok.sep_token or ""

    @property
    def pad_token(self) -> str:
        return self._tok.pad_token or ""

    @property
    def is_fast(self) -> bool:
        return True

    @property
    def vocab_size(self) -> int:
        return len(self._vocab)

    @property
    def max_token_id(self) -> int:
        return self._max_token_id

    @property
    def truncation_side(self) -> str:
        return getattr(self._tok, "truncation_side", "right")

    def __call__(self, *args, **kwargs):
        return self._tok(*args, **kwargs)

    def get_vocab(self) -> dict[str, int]:
        return dict(self._vocab)

    def get_added_vocab(self) -> dict[str, int]:
        return self._tok.get_added_vocab()

    def encode_one(
        self, text: str, truncation: bool = False, max_length: int | None = None
    ) -> list[int]:
        return self._tok.encode(
            text, add_special_tokens=False, truncation=truncation, max_length=max_length
        )

    def encode(
        self,
        text: str,
        truncation: bool | None = None,
        max_length: int | None = None,
        add_special_tokens: bool | None = None,
    ) -> list[int]:
        return self._tok.encode(
            text,
            truncation=truncation if truncation is not None else False,
            max_length=max_length,
            add_special_tokens=add_special_tokens if add_special_tokens is not None else False,
        )

    def apply_chat_template(
        self,
        messages,
        tools=None,
        **kwargs,
    ) -> list[int]:
        kwargs.setdefault("tokenize", True)
        return self._tok.apply_chat_template(messages, tools=tools, **kwargs)  # type: ignore[arg-type]

    def convert_tokens_to_string(self, tokens: list[str]) -> str:
        return self._tok.convert_tokens_to_string(tokens)

    def decode(self, ids: list[int] | int, skip_special_tokens: bool = True) -> str:
        return self._tok.decode(ids, skip_special_tokens=skip_special_tokens)

    def convert_ids_to_tokens(
        self, ids: list[int], skip_special_tokens: bool = True
    ) -> list[str]:
        return self._tok.convert_ids_to_tokens(ids, skip_special_tokens=skip_special_tokens)


if vllm_mistral is not None:

    _original_mistral_from_pretrained = (
        vllm_mistral.MistralTokenizer.from_pretrained.__func__
    )

    @classmethod
    def _patched_mistral_from_pretrained(
        cls, pretrained_model_name_or_path, *, revision=None
    ):
        try:
            return _original_mistral_from_pretrained(
                cls, pretrained_model_name_or_path, revision=revision
            )
        except KeyError as exc:
            if "config" not in str(exc):
                raise
            fast = GPTOSSTokenizerFast.from_pretrained(
                pretrained_model_name_or_path, revision=revision
            )
            return GPTOSSTokenizerShim(fast)

    vllm_mistral.MistralTokenizer.from_pretrained = classmethod(  # type: ignore[assignment]
        _patched_mistral_from_pretrained
    )
