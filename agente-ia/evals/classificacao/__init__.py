# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Evals de classificação: dataset JSONL → classificador injetável → relatório.

`Classificador` recebe o estado (lista de mensagens, como `estado_para_jev`
montaria) e devolve `{"intencao", "categoria", "prioridade", "confiancas"}`.
Hoje só existe o `classificador_nulo` (`outro`/`indefinida`/`media`, confiança 0):
ele prova o encanamento. TODO(E2.2): `--jev` e `--llm` passam a usar
`src.classificacao.classificar` com `JEVTypeSafe` / `FakeJEV` + fallback.
"""

from __future__ import annotations

import json
import time
from collections.abc import Callable, Mapping, Sequence
from pathlib import Path
from typing import Any, Final

from evals.formatos import (
    CasoClassificacao,
    ErroFormato,
    Taxonomia,
    ordem_prioridade,
    validar_casos,
)
from evals.relatorio import ParPrevisao, ResultadoCaso

Classificador = Callable[[list[str]], dict[str, Any]]

DIRETORIO: Final[Path] = Path(__file__).resolve().parent
FONTES_ARQUIVO: Final[Mapping[str, Path]] = {
    "sintetico": DIRETORIO / "sintetico.jsonl",
    "historico": DIRETORIO / "historico.jsonl",
}
LIMIARES_PADRAO: Final[Mapping[str, float]] = {
    "intencao": 0.70,  # JEV_LIMIAR_INTENCAO_ALTO (docs/plano/02)
    "categoria": 0.65,  # JEV_LIMIAR_CATEGORIA_ALTO
    "prioridade": 0.60,  # JEV_LIMIAR_PRIORIDADE
}


def classificador_nulo(estado: list[str]) -> dict[str, Any]:
    """Baseline trivial: sempre `outro`/`indefinida`/`media` com confiança 0 (prova o encanamento)."""
    return {
        "intencao": "outro",
        "categoria": "indefinida",
        "prioridade": "media",
        "confiancas": {"intencao": 0.0, "categoria": 0.0, "prioridade": 0.0},
    }


def obter_classificador(modo: str) -> tuple[Classificador, str]:
    """Devolve (classificador, nome) para `jev` | `llm` | `nulo`.

    TODO(E2.2): importar `src.classificacao` aqui (jev → JEVTypeSafe real,
    exige TYPESAFE_API_KEY; llm → fallback estruturado com FakeJEV indisponível).
    Até lá os dois modos caem no nulo, e o relatório registra isso.
    """
    if modo in {"jev", "llm", "nulo"}:
        return classificador_nulo, "nulo"
    raise ValueError(f"modo desconhecido: {modo}")


def carregar_dataset(caminho: Path, taxonomia: Taxonomia | None = None) -> list[CasoClassificacao]:
    """Lê o JSONL e valida cada linha; erros vêm com número da linha."""
    brutos: list[Any] = []
    with caminho.open(encoding="utf-8") as f:
        for numero, linha in enumerate(f, start=1):
            if not linha.strip():
                continue
            try:
                brutos.append(json.loads(linha))
            except json.JSONDecodeError as e:
                raise ErroFormato(f"{caminho.name}:{numero}", f"JSON inválido: {e.msg}") from e
    categorias = taxonomia.categorias if taxonomia else None
    try:
        return validar_casos(brutos, categorias)
    except ErroFormato as e:
        raise ErroFormato(f"{caminho.name} {e.campo}", e.motivo) from e


def _confianca(resposta: Mapping[str, Any], pergunta: str) -> float:
    confiancas = resposta.get("confiancas") or {}
    valor = confiancas.get(pergunta, 0.0)
    return float(valor) if isinstance(valor, int | float) else 0.0


def avaliar_caso(
    caso: CasoClassificacao, resposta: Mapping[str, Any], latencia_ms: float
) -> ResultadoCaso:
    """Converte a resposta do classificador nos pares por pergunta.

    - intenção e categoria: exatas;
    - prioridade: acerto se `previsto ≥ prioridade_min` (ordinal com piso);
      só avaliada quando o caso declara `prioridade_min`.
    - frustração: não avaliada aqui (o classificador não a devolve); TODO(E2.4).
    """
    perguntas: dict[str, ParPrevisao] = {}
    intencao = str(resposta.get("intencao", ""))
    categoria = str(resposta.get("categoria", ""))
    perguntas["intencao"] = ParPrevisao(
        esperado=caso.esperado.intencao,
        previsto=intencao,
        confianca=_confianca(resposta, "intencao"),
        acerto=intencao == caso.esperado.intencao,
    )
    perguntas["categoria"] = ParPrevisao(
        esperado=caso.esperado.categoria,
        previsto=categoria,
        confianca=_confianca(resposta, "categoria"),
        acerto=categoria == caso.esperado.categoria,
    )
    if caso.esperado.prioridade_min is not None:
        prioridade = str(resposta.get("prioridade", ""))
        try:
            acerto = ordem_prioridade(prioridade) >= ordem_prioridade(caso.esperado.prioridade_min)
        except ValueError:
            acerto = False
        perguntas["prioridade"] = ParPrevisao(
            esperado=caso.esperado.prioridade_min,
            previsto=prioridade,
            confianca=_confianca(resposta, "prioridade"),
            acerto=acerto,
            exata=False,
        )
    return ResultadoCaso(id=caso.id, perguntas=perguntas, latencia_ms=latencia_ms)


def rodar(
    casos: Sequence[CasoClassificacao],
    classificador: Classificador,
    relogio: Callable[[], float] = time.perf_counter,
) -> list[ResultadoCaso]:
    """Classifica cada caso medindo latência (relógio injetável para teste)."""
    resultados: list[ResultadoCaso] = []
    for caso in casos:
        inicio = relogio()
        resposta = classificador(list(caso.estado))
        latencia_ms = (relogio() - inicio) * 1000.0
        resultados.append(avaliar_caso(caso, resposta, latencia_ms))
    return resultados


def contar_por_categoria(casos: Sequence[CasoClassificacao]) -> dict[str, int]:
    contagem: dict[str, int] = {}
    for c in casos:
        contagem[c.esperado.categoria] = contagem.get(c.esperado.categoria, 0) + 1
    return dict(sorted(contagem.items()))
