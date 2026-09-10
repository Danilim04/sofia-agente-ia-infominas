"""Testes da fábrica de LLM/embeddings (Épico 5).

`ChatOpenAI` aceita ser instanciado sem fazer chamadas de rede, então os
testes inspecionam diretamente os atributos do cliente. `HuggingFaceEmbeddings`
faz download do modelo na inicialização — por isso `get_embeddings` é
testado via monkeypatch (a real instanciação acontece em `test_rag` com
um fake).
"""

from __future__ import annotations

import pytest
from langchain_openai import ChatOpenAI

from src.agent import llm as llm_factory
from src.config import get_settings


@pytest.fixture(autouse=True)
def _reset_factory_caches():
    def _limpar():
        llm_factory.get_llm.cache_clear()
        llm_factory.get_classifier_llm.cache_clear()
        llm_factory.get_embeddings.cache_clear()
        llm_factory.get_local_embeddings.cache_clear()
        get_settings.cache_clear()

    _limpar()
    yield
    _limpar()


# ---------------------------------------------------------------------------
# get_llm
# ---------------------------------------------------------------------------


def test_get_llm_retorna_chatopenai():
    client = llm_factory.get_llm()
    assert isinstance(client, ChatOpenAI)


def test_get_llm_aponta_para_openrouter():
    client = llm_factory.get_llm()
    settings = get_settings()
    # langchain_openai expõe a base_url como `openai_api_base`.
    assert client.openai_api_base == settings.openrouter_base_url
    assert "openrouter.ai" in client.openai_api_base


def test_get_llm_usa_model_e_temperature_do_settings():
    client = llm_factory.get_llm()
    settings = get_settings()
    assert client.model_name == settings.openrouter_model
    assert client.temperature == settings.llm_temperature


def test_get_llm_injeta_headers_de_boa_cidadania_openrouter():
    client = llm_factory.get_llm()
    settings = get_settings()
    headers = client.default_headers or {}
    assert headers.get("HTTP-Referer") == settings.app_referer
    assert headers.get("X-Title") == settings.app_title


def test_get_llm_e_cacheado_por_processo():
    a = llm_factory.get_llm()
    b = llm_factory.get_llm()
    assert a is b


# ---------------------------------------------------------------------------
# get_classifier_llm
# ---------------------------------------------------------------------------


def test_get_classifier_llm_usa_modelo_classifier_e_temp_zero():
    client = llm_factory.get_classifier_llm()
    settings = get_settings()
    assert isinstance(client, ChatOpenAI)
    assert client.model_name == settings.openrouter_classifier_model
    # Classificação tem que ser determinística.
    assert client.temperature == 0.0


def test_classifier_e_principal_sao_clientes_distintos():
    principal = llm_factory.get_llm()
    classifier = llm_factory.get_classifier_llm()
    assert principal is not classifier


def test_classifier_tambem_aponta_para_openrouter_com_headers():
    client = llm_factory.get_classifier_llm()
    settings = get_settings()
    assert client.openai_api_base == settings.openrouter_base_url
    headers = client.default_headers or {}
    assert headers.get("HTTP-Referer") == settings.app_referer
    assert headers.get("X-Title") == settings.app_title


# ---------------------------------------------------------------------------
# get_embeddings — sem baixar modelo de verdade
# ---------------------------------------------------------------------------


def test_get_embeddings_constroi_huggingface_com_settings(monkeypatch):
    """Captura os kwargs passados para `HuggingFaceEmbeddings` sem baixar nada."""
    capturado: dict = {}

    class FakeHFEmbeddings:
        def __init__(self, **kwargs):
            capturado.update(kwargs)

    import src.agent.llm as llm_mod

    # Precisamos interceptar o import tardio dentro de get_embeddings.
    import langchain_huggingface

    monkeypatch.setattr(
        langchain_huggingface, "HuggingFaceEmbeddings", FakeHFEmbeddings
    )

    out = llm_mod.get_embeddings()
    assert isinstance(out, FakeHFEmbeddings)

    settings = get_settings()
    assert capturado["model_name"] == settings.embeddings_model
    assert capturado["model_kwargs"] == {"device": "cpu"}
    assert capturado["encode_kwargs"] == {"normalize_embeddings": True}


def test_get_embeddings_openrouter_usa_endpoint_embeddings_com_texto_cru(monkeypatch):
    """Provider openrouter: OpenAIEmbeddings apontado para o OpenRouter, com a
    chave do LLM, headers de boa cidadania e SEM tokenização tiktoken
    (check_embedding_ctx_length=False — o OpenRouter recebe texto)."""
    capturado: dict = {}

    class FakeOpenAIEmbeddings:
        def __init__(self, **kwargs):
            capturado.update(kwargs)

    import langchain_openai

    monkeypatch.setattr(langchain_openai, "OpenAIEmbeddings", FakeOpenAIEmbeddings)
    monkeypatch.setenv("EMBEDDINGS_PROVIDER", "openrouter")
    monkeypatch.setenv("EMBEDDINGS_MODEL", "openai/text-embedding-3-small")
    get_settings.cache_clear()

    import src.agent.llm as llm_mod

    out = llm_mod.get_embeddings()
    assert isinstance(out, FakeOpenAIEmbeddings)
    settings = get_settings()
    assert capturado["model"] == "openai/text-embedding-3-small"
    assert capturado["openai_api_base"] == settings.openrouter_base_url
    assert capturado["openai_api_key"] == settings.openrouter_api_key
    assert capturado["default_headers"]["X-Title"] == settings.app_title
    assert capturado["check_embedding_ctx_length"] is False
    assert capturado["request_timeout"] == settings.llm_timeout


def test_get_embeddings_provider_invalido_falha_alto(monkeypatch):
    monkeypatch.setenv("EMBEDDINGS_PROVIDER", "cohere")
    get_settings.cache_clear()
    with pytest.raises(ValueError, match="EMBEDDINGS_PROVIDER"):
        llm_factory.get_embeddings()


def test_chroma_de_rollback_consulta_sempre_com_o_modelo_local(monkeypatch):
    """Mesmo com provider openrouter, o Chroma embutido usa o modelo local fixo
    com que foi gerado no build — misturar espaços vetoriais seria lixo mudo."""
    capturado: dict = {}

    class FakeHFEmbeddings:
        def __init__(self, **kwargs):
            capturado.update(kwargs)

    class FakeChroma:
        def __init__(self, **kwargs):
            capturado["embedding_function"] = kwargs["embedding_function"]

    import langchain_chroma
    import langchain_huggingface

    monkeypatch.setattr(langchain_huggingface, "HuggingFaceEmbeddings", FakeHFEmbeddings)
    monkeypatch.setattr(langchain_chroma, "Chroma", FakeChroma)
    monkeypatch.setenv("EMBEDDINGS_PROVIDER", "openrouter")
    monkeypatch.setenv("EMBEDDINGS_MODEL", "openai/text-embedding-3-small")
    get_settings.cache_clear()

    from src.rag.ingest import CHROMA_EMBEDDINGS_MODEL
    from src.rag.retriever import get_vector_store

    get_vector_store()
    assert isinstance(capturado["embedding_function"], FakeHFEmbeddings)
    assert capturado["model_name"] == CHROMA_EMBEDDINGS_MODEL


def test_retriever_reexporta_get_embeddings_do_llm_factory():
    """Garante o caminho de compatibilidade `from src.rag.retriever import get_embeddings`."""
    from src.rag.retriever import get_embeddings as embeddings_via_retriever

    assert embeddings_via_retriever is llm_factory.get_embeddings