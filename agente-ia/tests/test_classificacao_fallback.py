"""Fallback LLM da classificação: saída estruturada com as MESMAS classes da
taxonomia, fail-open e sem rede (E2.2).
"""

from __future__ import annotations

import asyncio
from typing import Any

import pytest
from pydantic import ValidationError

from src.classificacao.fallback_llm import (
    FallbackLLMEstruturado,
    construir_schema,
    montar_prompt_sistema,
)
from src.classificacao.tipos import INTENCOES
from tests.fakes import taxonomia_de_teste

TAX = taxonomia_de_teste()


class _Estruturado:
    def __init__(self, resultado: Any) -> None:
        self.resultado = resultado
        self.mensagens: list[Any] = []

    async def ainvoke(self, mensagens: list[Any]) -> Any:
        self.mensagens = mensagens
        if isinstance(self.resultado, BaseException):
            raise self.resultado
        return self.resultado


class _ChatFalso:
    def __init__(self, resultado: Any) -> None:
        self.estruturado = _Estruturado(resultado)
        self.schema: Any = None

    def with_structured_output(self, schema: Any) -> _Estruturado:
        self.schema = schema
        return self.estruturado


def _classificar(resultado: Any, estado: str = "Mensagem atual: minha net caiu"):
    chat = _ChatFalso(resultado)
    fb = FallbackLLMEstruturado(lambda: chat, TAX)
    return asyncio.run(fb.classificar(estado)), chat


def test_schema_aceita_so_as_classes_da_taxonomia_e_as_intencoes():
    schema = construir_schema(TAX)
    ok = schema(intencao="reclamacao_problema", categoria="sem_conexao")
    assert ok.categoria == "sem_conexao"  # type: ignore[attr-defined]
    with pytest.raises(ValidationError):
        schema(intencao="reclamacao_problema", categoria="categoria_inventada")
    with pytest.raises(ValidationError):
        schema(intencao="intencao_inventada", categoria="sem_conexao")
    assert set(schema.model_json_schema()["properties"]["intencao"]["enum"]) == set(INTENCOES)


def test_usa_saida_estruturada_com_o_schema_da_taxonomia():
    schema = construir_schema(TAX)
    r, chat = _classificar(schema(intencao="reclamacao_problema", categoria="lentidao"))
    assert (r.intencao, r.categoria, r.ok) == ("reclamacao_problema", "lentidao", True)
    assert chat.schema.__name__ == "ClassificacaoLLM"


def test_transcricao_vai_como_dado_delimitado_e_o_prompt_nao_tem_mesas():
    schema = construir_schema(TAX)
    _, chat = _classificar(schema(intencao="outro", categoria="indefinida"), "Mensagem atual: oi")
    sistema, humana = chat.estruturado.mensagens
    assert "<transcricao>" in humana.content and "Mensagem atual: oi" in humana.content
    assert "DADO" in sistema.content
    for categoria in TAX.nomes:
        assert categoria in sistema.content
    for cat in TAX.categorias.values():
        assert cat.mesa not in sistema.content and cat.departamento not in sistema.content


def test_prompt_pede_indefinida_em_vez_de_chute():
    assert "indefinida" in montar_prompt_sistema(TAX)
    assert "não chute" in montar_prompt_sistema(TAX)


def test_falha_do_llm_e_fail_open_para_outro_indefinida():
    r, _ = _classificar(RuntimeError("openrouter 402"))
    assert (r.intencao, r.categoria, r.ok) == ("outro", "indefinida", False)


def test_valores_fora_da_lista_viram_outro_e_indefinida_mesmo_sem_validacao_do_schema():
    class _Solto:
        intencao = "fazer_pix"
        categoria = "inventada"

    r, _ = _classificar(_Solto())
    assert (r.intencao, r.categoria, r.ok) == ("outro", "indefinida", True)
