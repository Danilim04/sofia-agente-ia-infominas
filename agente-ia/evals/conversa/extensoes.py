# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Extensões do formato de cenário de conversa (E3.5), vivem só em `evals/conversa/`.

O formato base (`evals/formatos.py`, validador estrito) cobre o que o TEMPLATE
mostra. Os cenários de E3.5 precisam de quatro coisas a mais, que este módulo
lê do YAML ANTES da validação base (tira as chaves daqui e entrega o resto):

Nível do cenário
  `relogio`            ISO 8601 com fuso (ex.: `2026-10-21T21:40:00-03:00`): "agora" do
                       cenário para a validade da proposta e o período do dia da
                       saudação. Default: `PADRAO_RELOGIO` (manhã de Brasília).
Nível do turno
  `avanca_minutos`     minutos que o relógio anda ANTES do turno (proposta vencida).
  `rag`                chunks que o retriever devolve NESTE turno
                       (`[{texto, source?, secao?}]`; ausente = base vazia).
  `gateway`            sobrescreve a resposta do gateway falso por caminho, neste
                       turno: `{ticket/consultar: erro-posse_nao_verificada}`; o valor é
                       um arquivo de `contratos/tools-infodesk/<caminho>/` sem `.json`
                       (`ok`, `erro-*`) ou `http_500` / `timeout` (falha de transporte).
Nível da `espera` (somam-se às de `CHAVES_ESPERA`)
  `prompt_contem` / `prompt_nao_contem`  trechos do system prompt que o LLM recebeu no turno
  `llm_chamadas`       quantas vezes o LLM foi chamado (0 = barrado antes dele)
  `gateway_json`       `{endpoint, campos: {"a.b": valor}}`: alguma chamada ao endpoint
                       levou esses campos (prova que a sessão foi injetada pelo código)
  `gateway_nao_recebeu`  lista de endpoints que NÃO podem ter sido chamados no turno
  `gateway_vezes`      `{endpoint: n}`: quantas chamadas o endpoint recebeu no turno
  `estado`             subconjunto do estado do grafo ao fim do turno (aninhado)
  `estado_nao_contem`  trechos que não podem aparecer no estado (exceto `messages`)

Promover estas chaves para `evals/formatos.py` é um follow-up (aquele arquivo é
compartilhado com a família de classificação, de outra etapa).
"""

from __future__ import annotations

import copy
import json
from collections.abc import Mapping
from dataclasses import dataclass, field, fields
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Final

import yaml

from evals.formatos import CenarioConversa, ErroFormato, validar_cenario

RAIZ_AGENTE_IA: Final[Path] = Path(__file__).resolve().parent.parent.parent
RAIZ_CONTRATOS: Final[Path] = RAIZ_AGENTE_IA.parent / "contratos" / "tools-infodesk"
GATEWAY_PREFIXO: Final[str] = "/tools/infodesk/"
#: "Agora" dos cenários sem `relogio`: manhã de Brasília (saudação "Bom dia").
PADRAO_RELOGIO: Final[datetime] = datetime(
    2026, 10, 21, 10, 0, tzinfo=timezone(timedelta(hours=-3))
)
#: Falhas de transporte que o gateway falso sabe simular (além dos JSON de contrato).
FALHAS_TRANSPORTE: Final[frozenset[str]] = frozenset({"http_500", "timeout"})

CHAVES_EXTRA_TURNO: Final[frozenset[str]] = frozenset({"rag", "gateway", "avanca_minutos"})
CHAVES_EXTRA_ESPERA: Final[frozenset[str]] = frozenset(
    {
        "prompt_contem",
        "prompt_nao_contem",
        "tool_viu_contem",
        "tool_viu_nao_contem",
        "llm_chamadas",
        "gateway_json",
        "gateway_nao_recebeu",
        "gateway_vezes",
        "estado",
        "estado_nao_contem",
    }
)


@dataclass(frozen=True)
class ExtrasTurno:
    rag: tuple[Mapping[str, Any], ...] = ()
    gateway: Mapping[str, str] = field(default_factory=dict)
    avanca_minutos: float = 0.0
    espera: Mapping[str, Any] = field(default_factory=dict)
    #: índice do passo do LLM -> tool calls emitidas JUNTAS numa só mensagem (`paralelo:`).
    paralelas: Mapping[int, tuple[tuple[str, Mapping[str, Any]], ...]] = field(default_factory=dict)


@dataclass(frozen=True)
class ExtrasCenario:
    relogio: datetime = PADRAO_RELOGIO
    turnos: tuple[ExtrasTurno, ...] = ()


@dataclass(frozen=True)
class CenarioEval(CenarioConversa):
    """`CenarioConversa` + as extensões deste módulo (mesmo contrato para quem já o usa)."""

    extras: ExtrasCenario = field(default_factory=ExtrasCenario)


@dataclass
class Observado:
    """O que o runner observou no turno (insumo das esperas estendidas)."""

    prompt_sistema: str = ""
    tool_textos: str = ""
    llm_chamadas: int = 0
    chamadas_gateway: list[dict[str, Any]] = field(default_factory=list)
    estado: dict[str, Any] = field(default_factory=dict)


# --------------------------------------------------------------------- carga
def caminhos_gateway() -> tuple[str, ...]:
    """Caminhos de gateway que têm contrato (`<área>/<ação>` com `ok.json`)."""
    return tuple(
        sorted(
            p.parent.relative_to(RAIZ_CONTRATOS).as_posix()
            for p in RAIZ_CONTRATOS.glob("*/*/ok.json")
        )
    )


def _dict(valor: object, campo: str) -> dict[str, Any]:
    if not isinstance(valor, dict):
        raise ErroFormato(campo, f"deve ser um mapeamento, veio {type(valor).__name__}")
    return valor


def _lista_de_texto(valor: object, campo: str) -> list[str]:
    if not isinstance(valor, list) or not valor:
        raise ErroFormato(campo, "deve ser uma lista não vazia de textos")
    for i, item in enumerate(valor):
        if not isinstance(item, str) or not item:
            raise ErroFormato(f"{campo}[{i}]", "deve ser texto não vazio")
    return list(valor)


def _validar_rag(valor: object, campo: str) -> tuple[Mapping[str, Any], ...]:
    if not isinstance(valor, list):
        raise ErroFormato(campo, "deve ser uma lista de chunks ([] = base sem resultado)")
    chunks: list[Mapping[str, Any]] = []
    for i, bruto in enumerate(valor):
        c = _dict(bruto, f"{campo}[{i}]")
        extras = sorted(set(c) - {"texto", "source", "secao"})
        if extras:
            raise ErroFormato(
                f"{campo}[{i}].{extras[0]}", "chave desconhecida (texto, source, secao)"
            )
        if not isinstance(c.get("texto"), str) or not c["texto"].strip():
            raise ErroFormato(f"{campo}[{i}].texto", "texto obrigatório")
        chunks.append({"source": "base-de-teste.md", **c})
    return tuple(chunks)


def _validar_gateway(valor: object, campo: str) -> dict[str, str]:
    d = _dict(valor, campo)
    validos = caminhos_gateway()
    saida: dict[str, str] = {}
    for caminho, nome in d.items():
        if caminho not in validos:
            raise ErroFormato(
                f"{campo}.{caminho}", f"caminho desconhecido (contratos: {list(validos)})"
            )
        if not isinstance(nome, str):
            raise ErroFormato(f"{campo}.{caminho}", "deve ser o nome do arquivo de contrato")
        if (
            nome not in FALHAS_TRANSPORTE
            and not (RAIZ_CONTRATOS / caminho / f"{nome}.json").is_file()
        ):
            existentes = sorted(p.stem for p in (RAIZ_CONTRATOS / caminho).glob("*.json"))
            raise ErroFormato(
                f"{campo}.{caminho}",
                f"'{nome}' não existe (contratos: {existentes}; falhas: {sorted(FALHAS_TRANSPORTE)})",
            )
        saida[caminho] = nome
    return saida


def _validar_espera_extra(bruto: Mapping[str, Any], campo: str) -> dict[str, Any]:
    e: dict[str, Any] = {}
    for chave in (
        "prompt_contem",
        "prompt_nao_contem",
        "tool_viu_contem",
        "tool_viu_nao_contem",
        "estado_nao_contem",
        "gateway_nao_recebeu",
    ):
        if chave in bruto:
            e[chave] = _lista_de_texto(bruto[chave], f"{campo}.{chave}")
    if "llm_chamadas" in bruto:
        n = bruto["llm_chamadas"]
        if isinstance(n, bool) or not isinstance(n, int) or n < 0:
            raise ErroFormato(f"{campo}.llm_chamadas", "deve ser inteiro >= 0")
        e["llm_chamadas"] = n
    if "gateway_json" in bruto:
        g = _dict(bruto["gateway_json"], f"{campo}.gateway_json")
        extras = sorted(set(g) - {"endpoint", "campos"})
        if extras:
            raise ErroFormato(
                f"{campo}.gateway_json.{extras[0]}", "chave desconhecida (endpoint, campos)"
            )
        if not isinstance(g.get("endpoint"), str):
            raise ErroFormato(f"{campo}.gateway_json.endpoint", "texto obrigatório")
        e["gateway_json"] = {
            "endpoint": g["endpoint"],
            "campos": _dict(g.get("campos"), f"{campo}.gateway_json.campos"),
        }
    if "gateway_vezes" in bruto:
        v = _dict(bruto["gateway_vezes"], f"{campo}.gateway_vezes")
        for endpoint, n in v.items():
            if isinstance(n, bool) or not isinstance(n, int) or n < 0:
                raise ErroFormato(f"{campo}.gateway_vezes.{endpoint}", "deve ser inteiro >= 0")
        e["gateway_vezes"] = dict(v)
    if "estado" in bruto:
        e["estado"] = _dict(bruto["estado"], f"{campo}.estado")
    return e


def _extrair_paralelas(
    llm: object, campo: str
) -> dict[int, tuple[tuple[str, Mapping[str, Any]], ...]]:
    """`- paralelo: [{tool, args}, ...]`: várias tool calls numa só mensagem do LLM.

    O passo vira, para o validador base, uma chamada simples da 1ª tool; as demais
    ficam aqui e o runner emite todas na mesma `AIMessage`.
    """
    saida: dict[int, tuple[tuple[str, Mapping[str, Any]], ...]] = {}
    if not isinstance(llm, list):
        return saida
    for j, passo in enumerate(llm):
        if not isinstance(passo, dict) or "paralelo" not in passo:
            continue
        onde = f"{campo}[{j}].paralelo"
        chamadas = passo["paralelo"]
        if not isinstance(chamadas, list) or len(chamadas) < 2:
            raise ErroFormato(onde, "deve ser uma lista com pelo menos 2 chamadas {tool, args}")
        lidas: list[tuple[str, Mapping[str, Any]]] = []
        for k, chamada in enumerate(chamadas):
            c = _dict(chamada, f"{onde}[{k}]")
            if set(c) - {"tool", "args"} or not isinstance(c.get("tool"), str):
                raise ErroFormato(f"{onde}[{k}]", "use apenas {tool: nome, args: {...}}")
            lidas.append((c["tool"], _dict(c.get("args", {}), f"{onde}[{k}].args")))
        saida[j] = tuple(lidas)
        llm[j] = {"tool": lidas[0][0], "args": dict(lidas[0][1])}
    return saida


def _extrair(bruto: dict[str, Any], campo: str) -> ExtrasCenario:
    """Tira as chaves estendidas de `bruto` (já copiado) e devolve o que foi lido."""
    relogio = PADRAO_RELOGIO
    if "relogio" in bruto:
        texto = bruto.pop("relogio")
        try:
            relogio = datetime.fromisoformat(str(texto))
        except ValueError as e:
            raise ErroFormato(f"{campo}.relogio", f"ISO 8601 inválido: {texto!r}") from e
        if relogio.tzinfo is None:
            raise ErroFormato(f"{campo}.relogio", "precisa de fuso (ex.: -03:00)")
    turnos_extras: list[ExtrasTurno] = []
    turnos = bruto.get("turnos")
    if isinstance(turnos, list):
        for i, turno in enumerate(turnos):
            c = f"{campo}.turnos[{i}]"
            if not isinstance(turno, dict):
                turnos_extras.append(ExtrasTurno())
                continue
            rag = _validar_rag(turno.pop("rag"), f"{c}.rag") if "rag" in turno else ()
            gateway = (
                _validar_gateway(turno.pop("gateway"), f"{c}.gateway") if "gateway" in turno else {}
            )
            paralelas = _extrair_paralelas(turno.get("llm"), f"{c}.llm")
            avanca = turno.pop("avanca_minutos", 0)
            if isinstance(avanca, bool) or not isinstance(avanca, int | float) or avanca < 0:
                raise ErroFormato(f"{c}.avanca_minutos", "deve ser número >= 0")
            espera_extra: dict[str, Any] = {}
            espera = turno.get("espera")
            if isinstance(espera, dict):
                brutas = {k: espera.pop(k) for k in sorted(CHAVES_EXTRA_ESPERA & set(espera))}
                espera_extra = _validar_espera_extra(brutas, f"{c}.espera")
                if not espera:
                    raise ErroFormato(
                        f"{c}.espera",
                        f"além de {sorted(espera_extra)}, inclua ao menos uma espera básica (ex.: nao_chama)",
                    )
            turnos_extras.append(
                ExtrasTurno(
                    rag=rag,
                    gateway=gateway,
                    avanca_minutos=float(avanca),
                    espera=espera_extra,
                    paralelas=paralelas,
                )
            )
    return ExtrasCenario(relogio=relogio, turnos=tuple(turnos_extras))


def carregar_cenario_eval(caminho: Path, categorias: frozenset[str] | None = None) -> CenarioEval:
    """Lê o YAML, separa as extensões e valida o resto com `evals.formatos`."""
    with caminho.open(encoding="utf-8") as f:
        try:
            lido = yaml.safe_load(f)
        except yaml.YAMLError as e:
            raise ErroFormato(caminho.name, f"YAML inválido: {e}") from e
    if not isinstance(lido, dict):
        validar_cenario(lido, campo=caminho.name, categorias=categorias, arquivo=caminho)
        raise AssertionError("inalcançável: validar_cenario recusa o que não é mapeamento")
    bruto = copy.deepcopy(lido)
    extras = _extrair(bruto, caminho.name)
    base = validar_cenario(bruto, campo=caminho.name, categorias=categorias, arquivo=caminho)
    if len(extras.turnos) != len(base.turnos):
        raise ErroFormato(caminho.name, "turnos inconsistentes")
    return CenarioEval(**{f.name: getattr(base, f.name) for f in fields(base)}, extras=extras)


# ------------------------------------------------------------------ esperas
def _caminho_pontilhado(dado: Any, caminho: str) -> Any:
    for parte in caminho.split("."):
        if not isinstance(dado, Mapping) or parte not in dado:
            return _AUSENTE
        dado = dado[parte]
    return dado


class _Ausente:
    def __repr__(self) -> str:
        return "<ausente>"


_AUSENTE: Final = _Ausente()


def _subconjunto(esperado: Any, atual: Any, caminho: str, falhas: list[str]) -> None:
    if isinstance(esperado, Mapping):
        if not isinstance(atual, Mapping):
            falhas.append(f"estado.{caminho}: esperado mapeamento, veio {atual!r}")
            return
        for chave, valor in esperado.items():
            _subconjunto(
                valor, atual.get(chave), f"{caminho}.{chave}" if caminho else str(chave), falhas
            )
    elif atual != esperado:
        falhas.append(f"estado.{caminho}={atual!r}, esperado {esperado!r}")


def verificar_extras(espera: Mapping[str, Any], obs: Observado, rotulo: str) -> list[str]:
    """Falhas das esperas estendidas (lista vazia = passou)."""
    falhas: list[str] = []
    prompt = obs.prompt_sistema
    for trecho in espera.get("prompt_contem", ()):
        if trecho not in prompt:
            falhas.append(
                f"{rotulo}: prompt_contem {trecho!r} — não está no system prompt do turno"
            )
    for trecho in espera.get("prompt_nao_contem", ()):
        if trecho in prompt:
            falhas.append(f"{rotulo}: prompt_nao_contem {trecho!r} — apareceu no system prompt")
    vistos_tool = obs.tool_textos
    for trecho in espera.get("tool_viu_contem", ()):
        if trecho not in vistos_tool:
            falhas.append(f"{rotulo}: tool_viu_contem {trecho!r} — o LLM não viu isso da tool")
    for trecho in espera.get("tool_viu_nao_contem", ()):
        if trecho in vistos_tool:
            falhas.append(f"{rotulo}: tool_viu_nao_contem {trecho!r} — chegou ao LLM")
    if "llm_chamadas" in espera and obs.llm_chamadas != espera["llm_chamadas"]:
        falhas.append(
            f"{rotulo}: llm_chamadas={espera['llm_chamadas']} mas foram {obs.llm_chamadas}"
        )
    vistos = [c["endpoint"] for c in obs.chamadas_gateway]
    for endpoint in espera.get("gateway_nao_recebeu", ()):
        if endpoint in vistos:
            falhas.append(f"{rotulo}: gateway_nao_recebeu {endpoint} — recebeu")
    for endpoint, n in (espera.get("gateway_vezes") or {}).items():
        if vistos.count(endpoint) != n:
            falhas.append(
                f"{rotulo}: gateway_vezes {endpoint}={n} mas recebeu {vistos.count(endpoint)}x"
            )
    if "gateway_json" in espera:
        g = espera["gateway_json"]
        candidatas = [c for c in obs.chamadas_gateway if c["endpoint"] == g["endpoint"]]
        if not candidatas:
            falhas.append(
                f"{rotulo}: gateway_json {g['endpoint']} — nenhuma chamada (recebeu {vistos})"
            )
        for c in candidatas:  # TODAS as chamadas ao endpoint: uma errada entre as certas reprova
            erros = [
                f"{k}={_caminho_pontilhado(c['json'], k)!r}, esperado {v!r}"
                for k, v in g["campos"].items()
                if _caminho_pontilhado(c["json"], k) != v
            ]
            if erros:
                falhas.append(f"{rotulo}: gateway_json {g['endpoint']} — " + "; ".join(erros))
    if "estado" in espera:
        sub: list[str] = []
        _subconjunto(espera["estado"], obs.estado, "", sub)
        falhas.extend(f"{rotulo}: {s}" for s in sub)
    if espera.get("estado_nao_contem"):
        serial = json.dumps(
            {k: v for k, v in obs.estado.items() if k != "messages"},
            ensure_ascii=False,
            default=str,
        )
        for trecho in espera["estado_nao_contem"]:
            if trecho in serial:
                falhas.append(f"{rotulo}: estado_nao_contem {trecho!r} — está no estado persistido")
    return falhas
