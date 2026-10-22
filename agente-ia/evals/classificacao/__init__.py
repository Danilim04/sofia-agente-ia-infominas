# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Evals de classificação: dataset JSONL → classificador injetável → relatório.

`Classificador` recebe o estado (lista de mensagens, como `estado_para_jev`
montaria) e devolve `{"intencao", "categoria", "prioridade", "confiancas"}` mais,
opcionalmente, `frustracao`, `malicioso`, `fora_de_escopo`, `final`, `latencia_ms`
e `extras` (ver `adaptador.py`). Modos de `obter_classificador`:

- `jev`: `classificar()` real com o JEV real (`TYPESAFE_API_KEY`, P8);
- `llm`: `classificar()` real só com o fallback LLM real (`OPENROUTER_API_KEY`);
- `offline`: `classificar()` real com `JEVSimulado`/`FallbackSimulado` — sem rede,
  roda no CI; mede o encanamento, NÃO o desempenho do JEV;
- `nulo`: sempre `outro`/`indefinida`/`media` com confiança 0 (smoke).
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
    "prioridade_final": 0.60,
    "frustracao": 0.60,  # JEV_LIMIAR_FRUSTRACAO_CONFIANCA
    "categoria_final": 0.65,  # a final herda a confiança da categoria
    "malicioso": 0.60,  # |2p−1| ≥ 0.6 ⇔ p ≥ 0.8 ou p ≤ 0.2 (JEV_LIMIAR_MALICIOSO_BLOQUEIO)
    "fora_de_escopo": 0.60,  # idem (JEV_LIMIAR_FORA_ESCOPO)
}
JANELAS_PADRAO: Final[tuple[int, ...]] = (1, 3, 6, 10)  # C-JEV1 (docs/plano/02)


def classificador_nulo(estado: list[str]) -> dict[str, Any]:
    """Baseline trivial: sempre `outro`/`indefinida`/`media` com confiança 0 (prova o encanamento)."""
    return {
        "intencao": "outro",
        "categoria": "indefinida",
        "prioridade": "media",
        "confiancas": {"intencao": 0.0, "categoria": 0.0, "prioridade": 0.0},
    }


def obter_classificador(modo: str, janela: int | None = None) -> tuple[Classificador, str]:
    """Devolve (classificador, nome) para `jev` | `llm` | `offline` | `nulo`.

    `janela` = nº de mensagens enviadas ao JEV (`JEV_JANELA_MSGS`, C-JEV1); `None` =
    a configurada. Levanta `ErroModo` se faltar chave/SDK e `ValueError` se o modo
    não existir. O classificador tem `.fechar()` quando abre recursos (event loop).
    """
    if modo == "nulo":
        return classificador_nulo, "nulo"
    if modo in {"jev", "llm", "offline"}:
        from evals.classificacao.adaptador import criar_classificador

        classificador, descricao = criar_classificador(modo, janela)
        return classificador, descricao.classificador
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


def _probabilidade(resposta: Mapping[str, Any], chave: str) -> float | None:
    valor = resposta.get(chave)
    if isinstance(valor, bool) or not isinstance(valor, int | float):
        return None
    return float(valor)


def _par_noul(
    probabilidade: float, minimo: float | None, maximo: float | None
) -> ParPrevisao | None:
    """Noul como pergunta binária: `sim` (≥ min esperado) × `nao` (≤ max esperado).

    A confiança é `|2p − 1|` (docs/plano/02: o Noul não traz `confidence`).
    """
    confianca = abs(2.0 * probabilidade - 1.0)
    if minimo is not None:
        acerto = probabilidade >= minimo
        return ParPrevisao("sim", "sim" if acerto else "nao", confianca, acerto)
    if maximo is not None:
        acerto = probabilidade <= maximo
        return ParPrevisao("nao", "nao" if acerto else "sim", confianca, acerto)
    return None


def avaliar_caso(
    caso: CasoClassificacao, resposta: Mapping[str, Any], latencia_ms: float
) -> ResultadoCaso:
    """Converte a resposta do classificador nos pares por pergunta.

    - intenção e categoria: exatas (a resposta bruta do JEV; ver `adaptador.py`);
    - prioridade: nível bruto do JEV; acerto se `previsto ≥ prioridade_min` (ordinal com
      piso); só avaliada quando o caso declara `prioridade_min`. `prioridade_final` é a
      mesma conta sobre a prioridade que o roteador fixou (default da categoria + piso);
    - frustração: acerto se `frustracao_min ≤ previsto ≤ frustracao_max`; só avaliada
      quando o caso declara um dos limites e o classificador devolveu o nível;
    - malicioso / fora_de_escopo: binárias — `*_min` (esperado alto) ou `*_max`
      (esperado baixo); casos sem nenhum dos dois não entram;
    - categoria_final: a categoria que o roteador fixou, só nos casos de
      `reclamacao_problema` (nos demais o roteador nem classifica categoria).
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
    final = resposta.get("final")
    if isinstance(final, Mapping) and caso.esperado.intencao == "reclamacao_problema":
        categoria_final = str(final.get("categoria", ""))
        perguntas["categoria_final"] = ParPrevisao(
            esperado=caso.esperado.categoria,
            previsto=categoria_final,
            confianca=float(final.get("confianca", 0.0) or 0.0),
            acerto=categoria_final == caso.esperado.categoria,
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
    if isinstance(final, Mapping) and caso.esperado.prioridade_min is not None:
        prio_final = str(final.get("prioridade", ""))
        try:
            acerto_final = ordem_prioridade(prio_final) >= ordem_prioridade(
                caso.esperado.prioridade_min
            )
        except ValueError:
            acerto_final = False
        perguntas["prioridade_final"] = ParPrevisao(
            esperado=caso.esperado.prioridade_min,
            previsto=prio_final,
            confianca=float(final.get("confianca_prioridade", 0.0) or 0.0),
            acerto=acerto_final,
            exata=False,
        )
    nivel = resposta.get("frustracao")
    minimo, maximo = caso.esperado.frustracao_min, caso.esperado.frustracao_max
    if isinstance(nivel, int) and not isinstance(nivel, bool) and (minimo, maximo) != (None, None):
        perguntas["frustracao"] = ParPrevisao(
            esperado=f"{minimo if minimo is not None else 0}..{maximo if maximo is not None else 3}",
            previsto=str(nivel),
            confianca=_confianca(resposta, "frustracao"),
            acerto=(minimo is None or nivel >= minimo) and (maximo is None or nivel <= maximo),
            exata=False,
        )
    for pergunta, minimo_p, maximo_p in (
        ("malicioso", caso.esperado.malicioso_min, caso.esperado.malicioso_max),
        ("fora_de_escopo", caso.esperado.fora_de_escopo_min, caso.esperado.fora_de_escopo_max),
    ):
        probabilidade = _probabilidade(resposta, pergunta)
        if probabilidade is not None:
            par = _par_noul(probabilidade, minimo_p, maximo_p)
            if par is not None:
                perguntas[pergunta] = par
    extras = resposta.get("extras")
    return ResultadoCaso(
        id=caso.id,
        perguntas=perguntas,
        latencia_ms=latencia_ms,
        extras=dict(extras) if isinstance(extras, Mapping) else {},
    )


def rodar(
    casos: Sequence[CasoClassificacao],
    classificador: Classificador,
    relogio: Callable[[], float] = time.perf_counter,
) -> list[ResultadoCaso]:
    """Classifica cada caso medindo latência (relógio injetável para teste).

    Se o classificador informa `latencia_ms` (o adaptador mede só a chamada, ou
    devolve a latência simulada do offline), vale a dele; senão vale o relógio.
    """
    resultados: list[ResultadoCaso] = []
    for caso in casos:
        inicio = relogio()
        resposta = classificador(list(caso.estado))
        latencia_ms = (relogio() - inicio) * 1000.0
        informada = resposta.get("latencia_ms")
        if isinstance(informada, int | float) and not isinstance(informada, bool):
            latencia_ms = float(informada)
        resultados.append(avaliar_caso(caso, resposta, latencia_ms))
    return resultados


def contar_por_categoria(casos: Sequence[CasoClassificacao]) -> dict[str, int]:
    contagem: dict[str, int] = {}
    for c in casos:
        contagem[c.esperado.categoria] = contagem.get(c.esperado.categoria, 0) + 1
    return dict(sorted(contagem.items()))
