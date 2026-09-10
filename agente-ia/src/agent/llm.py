"""Fábricas de LLM e embeddings (Épico 5).

Centraliza a criação dos clientes LLM apontando para o OpenRouter
(`https://openrouter.ai/api/v1`) via `ChatOpenAI`, e provê os embeddings do
RAG — locais (sentence-transformers) ou pelo /embeddings do OpenRouter,
conforme EMBEDDINGS_PROVIDER. Mantém os clientes cacheados para reuso entre
chamadas dentro do mesmo processo.

Headers de boa cidadania exigidos pelo OpenRouter (`HTTP-Referer` e
`X-Title`) são injetados automaticamente a partir das settings — eles
ajudam o OpenRouter a roteamento e ranking, e identificam o app na
dashboard.
"""

from __future__ import annotations

from functools import lru_cache
from typing import TYPE_CHECKING, Optional

from src.config import Settings, get_settings

if TYPE_CHECKING:
    from langchain_core.embeddings import Embeddings
    from langchain_core.language_models.chat_models import BaseChatModel


def _openrouter_headers(settings: Settings) -> dict[str, str]:
    return {
        "HTTP-Referer": settings.app_referer,
        "X-Title": settings.app_title,
    }


def _build_chat_openrouter(
    model: str,
    temperature: float,
    settings: Optional[Settings] = None,
) -> "BaseChatModel":
    from langchain_openai import ChatOpenAI

    settings = settings or get_settings()
    return ChatOpenAI(
        base_url=settings.openrouter_base_url,
        api_key=settings.openrouter_api_key,
        model=model,
        temperature=temperature,
        default_headers=_openrouter_headers(settings),
        # Timeout por chamada + 1 retry: uma chamada pendurada não pode estourar
        # o BRAIN_TIMEOUT do gateway (60s) e virar erro genérico pro cliente.
        timeout=settings.llm_timeout,
        max_retries=1,
    )


@lru_cache(maxsize=1)
def get_llm() -> "BaseChatModel":
    """LLM principal do agente (suporta tool-calling)."""
    settings = get_settings()
    return _build_chat_openrouter(
        model=settings.openrouter_model,
        temperature=settings.llm_temperature,
        settings=settings,
    )


@lru_cache(maxsize=1)
def get_classifier_llm() -> "BaseChatModel":
    """LLM barato/rápido usado pelo classificador de segurança (Épico 6).

    Temperature fixa em 0.0 — classificação é determinística por design.
    """
    settings = get_settings()
    return _build_chat_openrouter(
        model=settings.openrouter_classifier_model,
        temperature=0.0,
        settings=settings,
    )


EMBEDDINGS_PROVIDERS = ("local", "openrouter")


@lru_cache(maxsize=4)
def get_local_embeddings(model_name: str) -> "Embeddings":
    """Embeddings locais via `sentence-transformers` em CPU (sem rede).

    Usado pelo provider "local" e SEMPRE pelo Chroma embutido na imagem
    (`CHROMA_EMBEDDINGS_MODEL`) — o índice de rollback não depende de API.
    """
    from langchain_huggingface import HuggingFaceEmbeddings

    return HuggingFaceEmbeddings(
        model_name=model_name,
        model_kwargs={"device": "cpu"},
        encode_kwargs={"normalize_embeddings": True},
    )


def _build_openrouter_embeddings(settings: Settings) -> "Embeddings":
    """Embeddings pelo endpoint /embeddings do OpenRouter (mesma chave do LLM)."""
    from langchain_openai import OpenAIEmbeddings

    return OpenAIEmbeddings(
        model=settings.embeddings_model,
        openai_api_key=settings.openrouter_api_key,
        openai_api_base=settings.openrouter_base_url,
        default_headers=_openrouter_headers(settings),
        # O OpenRouter recebe TEXTO. Por default o cliente OpenAI tokeniza com
        # tiktoken e manda arrays de ids — o OpenRouter não aceita isso.
        check_embedding_ctx_length=False,
        # Lote por requisição (o sync embute dezenas de chunks de uma vez).
        chunk_size=256,
        request_timeout=settings.llm_timeout,
        max_retries=2,
    )


@lru_cache(maxsize=1)
def get_embeddings() -> "Embeddings":
    """Embeddings do índice pgvector, conforme EMBEDDINGS_PROVIDER.

    "local" (default; dev/testes, offline) ou "openrouter" (produção:
    openai/text-embedding-3-small, multilíngue, ~US$0,02 por 1M tokens). É o
    ponto único de troca: sync, retrieval e warm-up passam por aqui.
    """
    settings = get_settings()
    provider = settings.embeddings_provider.strip().lower()
    if provider not in EMBEDDINGS_PROVIDERS:
        raise ValueError(
            f"EMBEDDINGS_PROVIDER inválido: {settings.embeddings_provider!r} "
            f"(aceitos: {', '.join(EMBEDDINGS_PROVIDERS)})"
        )
    if provider == "openrouter":
        return _build_openrouter_embeddings(settings)
    return get_local_embeddings(settings.embeddings_model)