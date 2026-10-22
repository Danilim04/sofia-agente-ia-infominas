# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Formatos dos datasets de eval, com validação que aponta campo e motivo.

Dois formatos (docs/plano/02 "Dataset de avaliação" e docs/plano/04 "Como
escrever um eval"):

- `CasoClassificacao` — uma linha de `evals/classificacao/*.jsonl`.
- `CenarioConversa` — um arquivo de `evals/conversa/*.yaml`.

Toda função `validar_*` recebe o dado cru (dict vindo de JSON/YAML) e devolve
o objeto tipado ou levanta `ErroFormato(campo, motivo)`. O `campo` é o caminho
completo (`turnos[2].espera.chama[0]`) para o autor do eval achar o erro.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Final

import yaml

# ---------------------------------------------------------------- vocabulário
# Espelha docs/plano/02 (perguntas ao JEV). Quando E2.2 criar
# src/classificacao/perguntas.py, estes conjuntos devem bater com ele
# (TODO(E2.2): teste de consistência evals × perguntas.py).
INTENCOES: Final[frozenset[str]] = frozenset(
    {
        "reclamacao_problema",
        "duvida_informacao",
        "status_chamado",
        "pedir_atendente",
        "saudacao_social",
        "confirmacao",
        "outro",
    }
)
PRIORIDADES: Final[tuple[str, ...]] = ("baixa", "media", "alta", "urgente")
FRUSTRACAO_MIN: Final[int] = 0
FRUSTRACAO_MAX: Final[int] = 3
FONTES: Final[frozenset[str]] = frozenset({"sintetico", "historico"})
PERGUNTAS_JEV: Final[frozenset[str]] = frozenset(
    {"intencao", "categoria", "prioridade", "frustracao", "malicioso", "fora_de_escopo"}
)
CHAVES_ESPERA: Final[frozenset[str]] = frozenset(
    {
        "chama",
        "nao_chama",
        "transfere",
        "contem",
        "nao_contem",
        "bolhas",
        "proposta_no_estado",
        "gateway_recebeu",
        "classificacao",
        "bloqueado",
    }
)

RAIZ_AGENTE_IA: Final[Path] = Path(__file__).resolve().parent.parent
CAMINHO_TAXONOMIA: Final[Path] = RAIZ_AGENTE_IA / "config" / "taxonomia.yaml"


class ErroFormato(ValueError):
    """Dado de eval inválido. `campo` é o caminho até o problema."""

    def __init__(self, campo: str, motivo: str) -> None:
        self.campo = campo
        self.motivo = motivo
        super().__init__(f"{campo}: {motivo}")


def ordem_prioridade(nome: str) -> int:
    """Posição ordinal de uma prioridade (baixa=0 … urgente=3)."""
    return PRIORIDADES.index(nome)


# ------------------------------------------------------------------ taxonomia
@dataclass(frozen=True)
class Taxonomia:
    """Subconjunto de `config/taxonomia.yaml` que os evals precisam."""

    categorias: frozenset[str]
    placeholder: bool
    versao: str
    prioridade_minima: Mapping[str, str] = field(default_factory=dict)


def carregar_taxonomia(caminho: Path = CAMINHO_TAXONOMIA) -> Taxonomia:
    """Lê a taxonomia e valida o mínimo de que os evals dependem."""
    with caminho.open(encoding="utf-8") as f:
        bruto = yaml.safe_load(f)
    if not isinstance(bruto, dict):
        raise ErroFormato("taxonomia", "arquivo deve ser um mapeamento YAML")
    categorias = bruto.get("categorias")
    if not isinstance(categorias, dict) or not categorias:
        raise ErroFormato("taxonomia.categorias", "mapeamento não vazio obrigatório")
    if "indefinida" not in categorias:
        raise ErroFormato("taxonomia.categorias", "categoria 'indefinida' obrigatória")
    minimas: dict[str, str] = {}
    for nome, corpo in categorias.items():
        if not isinstance(corpo, dict):
            raise ErroFormato(f"taxonomia.categorias.{nome}", "deve ser um mapeamento")
        for obrigatorio in ("descricao", "mesa", "departamento"):
            if not isinstance(corpo.get(obrigatorio), str) or not corpo[obrigatorio]:
                raise ErroFormato(f"taxonomia.categorias.{nome}.{obrigatorio}", "texto obrigatório")
        minima = corpo.get("prioridade_minima")
        if minima is not None:
            if minima not in PRIORIDADES:
                raise ErroFormato(
                    f"taxonomia.categorias.{nome}.prioridade_minima",
                    f"deve ser uma de {list(PRIORIDADES)}",
                )
            minimas[str(nome)] = str(minima)
    return Taxonomia(
        categorias=frozenset(str(c) for c in categorias),
        placeholder=bool(bruto.get("placeholder", False)),
        versao=str(bruto.get("versao", "")),
        prioridade_minima=minimas,
    )


# ---------------------------------------------------------- caso de classificação
@dataclass(frozen=True)
class Esperado:
    intencao: str
    categoria: str
    prioridade_min: str | None = None  # piso: acerto se previsto ≥
    frustracao_min: int | None = None  # piso (é o que dispara transferir ≥ 3)
    frustracao_max: int | None = None  # teto (cliente calmo)
    malicioso_min: float | None = None  # noul mínimo esperado (injeção)
    fora_de_escopo_min: float | None = None  # noul mínimo esperado (off-topic)


@dataclass(frozen=True)
class CasoClassificacao:
    id: str
    fonte: str
    estado: tuple[str, ...]
    esperado: Esperado
    notas: str | None = None
    rotulador: str | None = None


def _exigir_dict(valor: object, campo: str) -> dict[str, Any]:
    if not isinstance(valor, dict):
        raise ErroFormato(campo, f"deve ser um mapeamento, veio {type(valor).__name__}")
    return valor


def _exigir_texto(valor: object, campo: str, *, vazio_ok: bool = False) -> str:
    if not isinstance(valor, str):
        raise ErroFormato(campo, f"deve ser texto, veio {type(valor).__name__}")
    if not vazio_ok and not valor.strip():
        raise ErroFormato(campo, "não pode ser vazio")
    return valor


def _exigir_lista(valor: object, campo: str, *, vazia_ok: bool = False) -> list[Any]:
    if not isinstance(valor, list):
        raise ErroFormato(campo, f"deve ser uma lista, veio {type(valor).__name__}")
    if not vazia_ok and not valor:
        raise ErroFormato(campo, "lista não pode ser vazia")
    return valor


def _rejeitar_chaves(dado: Mapping[str, Any], permitidas: frozenset[str], campo: str) -> None:
    extras = sorted(set(dado) - permitidas)
    if extras:
        raise ErroFormato(
            f"{campo}.{extras[0]}", f"chave desconhecida (permitidas: {sorted(permitidas)})"
        )


_CHAVES_CASO: Final[frozenset[str]] = frozenset(
    {"id", "fonte", "estado", "esperado", "notas", "rotulador"}
)
_CHAVES_ESPERADO: Final[frozenset[str]] = frozenset(
    {
        "intencao",
        "categoria",
        "prioridade_min",
        "frustracao_min",
        "frustracao_max",
        "malicioso_min",
        "fora_de_escopo_min",
    }
)


def _nivel_opcional(valor: object, campo: str) -> int | None:
    if valor is None:
        return None
    if (
        isinstance(valor, bool)
        or not isinstance(valor, int)
        or not FRUSTRACAO_MIN <= valor <= FRUSTRACAO_MAX
    ):
        raise ErroFormato(campo, f"deve ser inteiro entre {FRUSTRACAO_MIN} e {FRUSTRACAO_MAX}")
    return valor


def _prob_opcional(valor: object, campo: str) -> float | None:
    if valor is None:
        return None
    if (
        isinstance(valor, bool)
        or not isinstance(valor, int | float)
        or not 0.0 <= float(valor) <= 1.0
    ):
        raise ErroFormato(campo, "deve ser número entre 0 e 1")
    return float(valor)


def validar_esperado(
    dado: object, campo: str, categorias: frozenset[str] | None = None
) -> Esperado:
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, _CHAVES_ESPERADO, campo)
    intencao = _exigir_texto(d.get("intencao"), f"{campo}.intencao")
    if intencao not in INTENCOES:
        raise ErroFormato(f"{campo}.intencao", f"'{intencao}' não está em {sorted(INTENCOES)}")
    categoria = _exigir_texto(d.get("categoria"), f"{campo}.categoria")
    if categorias is not None and categoria not in categorias:
        raise ErroFormato(
            f"{campo}.categoria", f"'{categoria}' não existe na taxonomia ({sorted(categorias)})"
        )
    prioridade_min = d.get("prioridade_min")
    if prioridade_min is not None and prioridade_min not in PRIORIDADES:
        raise ErroFormato(
            f"{campo}.prioridade_min", f"'{prioridade_min}' deve ser uma de {list(PRIORIDADES)}"
        )
    frustracao_min = _nivel_opcional(d.get("frustracao_min"), f"{campo}.frustracao_min")
    frustracao_max = _nivel_opcional(d.get("frustracao_max"), f"{campo}.frustracao_max")
    if (
        frustracao_min is not None
        and frustracao_max is not None
        and frustracao_min > frustracao_max
    ):
        raise ErroFormato(f"{campo}.frustracao_min", "não pode ser maior que frustracao_max")
    malicioso_min = _prob_opcional(d.get("malicioso_min"), f"{campo}.malicioso_min")
    fora_de_escopo_min = _prob_opcional(d.get("fora_de_escopo_min"), f"{campo}.fora_de_escopo_min")
    return Esperado(
        intencao=intencao,
        categoria=categoria,
        prioridade_min=prioridade_min,
        frustracao_min=frustracao_min,
        frustracao_max=frustracao_max,
        malicioso_min=malicioso_min,
        fora_de_escopo_min=fora_de_escopo_min,
    )


def validar_caso(
    dado: object, campo: str = "caso", categorias: frozenset[str] | None = None
) -> CasoClassificacao:
    """Valida uma linha do JSONL. `categorias` (da taxonomia) é opcional."""
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, _CHAVES_CASO, campo)
    id_ = _exigir_texto(d.get("id"), f"{campo}.id")
    fonte = _exigir_texto(d.get("fonte"), f"{campo}.fonte")
    if fonte not in FONTES:
        raise ErroFormato(f"{campo}.fonte", f"'{fonte}' deve ser uma de {sorted(FONTES)}")
    estado = _exigir_lista(d.get("estado"), f"{campo}.estado")
    msgs: list[str] = []
    for i, msg in enumerate(estado):
        msgs.append(_exigir_texto(msg, f"{campo}.estado[{i}]"))
    esperado = validar_esperado(d.get("esperado"), f"{campo}.esperado", categorias)
    notas = d.get("notas")
    if notas is not None:
        notas = _exigir_texto(notas, f"{campo}.notas", vazio_ok=True)
    rotulador = d.get("rotulador")
    if rotulador is not None:
        rotulador = _exigir_texto(rotulador, f"{campo}.rotulador")
    if fonte == "historico" and rotulador is None:
        raise ErroFormato(f"{campo}.rotulador", "obrigatório quando fonte == 'historico'")
    return CasoClassificacao(
        id=id_,
        fonte=fonte,
        estado=tuple(msgs),
        esperado=esperado,
        notas=notas,
        rotulador=rotulador,
    )


def validar_casos(
    dados: Sequence[object], categorias: frozenset[str] | None = None
) -> list[CasoClassificacao]:
    """Valida todas as linhas e exige ids únicos."""
    casos: list[CasoClassificacao] = []
    vistos: dict[str, int] = {}
    for i, dado in enumerate(dados):
        caso = validar_caso(dado, f"casos[{i}]", categorias)
        if caso.id in vistos:
            raise ErroFormato(
                f"casos[{i}].id", f"'{caso.id}' repetido (já em casos[{vistos[caso.id]}])"
            )
        vistos[caso.id] = i
        casos.append(caso)
    return casos


# ------------------------------------------------------------ cenário de conversa
@dataclass(frozen=True)
class Sessao:
    telefone: str
    nome_contato: str | None = None
    contato: str | None = None


@dataclass(frozen=True)
class RespostaJEV:
    """Resposta scriptada do FakeJEV em um turno (docs/plano/04)."""

    intencao: tuple[str, float]
    categoria: tuple[str, float]
    prioridade: tuple[int, float]
    frustracao: tuple[int, float]
    malicioso: float
    fora_de_escopo: float


@dataclass(frozen=True)
class PassoLLM:
    """Um item do script do LLM falso: chamada de tool OU texto final."""

    tool: str | None = None
    args: Mapping[str, Any] = field(default_factory=dict)
    pular: bool = False
    texto: str | None = None


@dataclass(frozen=True)
class Turno:
    cliente: str
    llm: tuple[PassoLLM, ...]
    espera: Mapping[str, Any]


@dataclass(frozen=True)
class CenarioConversa:
    id: str
    descricao: str
    sessao: Sessao
    jev: tuple[RespostaJEV, ...]
    turnos: tuple[Turno, ...]
    arquivo: Path | None = None
    #: Motivo de um xfail estrito: o cenário descreve o comportamento CORRETO e a
    #: Sofia ainda não o tem (limitação real). Quando passar, o teste falha e o
    #: marcador deve sair.
    xfail: str | None = None


_CHAVES_CENARIO: Final[frozenset[str]] = frozenset(
    {"id", "descricao", "sessao", "jev", "turnos", "xfail"}
)
_CHAVES_SESSAO: Final[frozenset[str]] = frozenset({"telefone", "nome_contato", "contato"})
_CHAVES_PASSO: Final[frozenset[str]] = frozenset({"tool", "args", "pular", "texto"})
_CHAVES_TURNO: Final[frozenset[str]] = frozenset({"cliente", "llm", "espera"})


def _prob(valor: object, campo: str) -> float:
    if isinstance(valor, bool) or not isinstance(valor, int | float):
        raise ErroFormato(campo, "deve ser número entre 0 e 1")
    if not 0.0 <= float(valor) <= 1.0:
        raise ErroFormato(campo, f"deve estar entre 0 e 1, veio {valor}")
    return float(valor)


def _par_choice(valor: object, campo: str, opcoes: frozenset[str] | None) -> tuple[str, float]:
    par = _exigir_lista(valor, campo)
    if len(par) != 2:
        raise ErroFormato(campo, "deve ser [opcao, confianca]")
    opcao = _exigir_texto(par[0], f"{campo}[0]")
    if opcoes is not None and opcao not in opcoes:
        raise ErroFormato(f"{campo}[0]", f"'{opcao}' não está em {sorted(opcoes)}")
    return opcao, _prob(par[1], f"{campo}[1]")


def _par_score(valor: object, campo: str, maximo: int) -> tuple[int, float]:
    par = _exigir_lista(valor, campo)
    if len(par) != 2:
        raise ErroFormato(campo, "deve ser [score, confianca]")
    score = par[0]
    if isinstance(score, bool) or not isinstance(score, int) or not 0 <= score <= maximo:
        raise ErroFormato(f"{campo}[0]", f"score deve ser inteiro entre 0 e {maximo}")
    return score, _prob(par[1], f"{campo}[1]")


def validar_resposta_jev(
    dado: object, campo: str, categorias: frozenset[str] | None = None
) -> RespostaJEV:
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, PERGUNTAS_JEV, campo)
    faltando = sorted(PERGUNTAS_JEV - set(d))
    if faltando:
        raise ErroFormato(f"{campo}.{faltando[0]}", "pergunta obrigatória ausente")
    return RespostaJEV(
        intencao=_par_choice(d["intencao"], f"{campo}.intencao", INTENCOES),
        categoria=_par_choice(d["categoria"], f"{campo}.categoria", categorias),
        prioridade=_par_score(d["prioridade"], f"{campo}.prioridade", len(PRIORIDADES) - 1),
        frustracao=_par_score(d["frustracao"], f"{campo}.frustracao", FRUSTRACAO_MAX),
        malicioso=_prob(d["malicioso"], f"{campo}.malicioso"),
        fora_de_escopo=_prob(d["fora_de_escopo"], f"{campo}.fora_de_escopo"),
    )


def _validar_passo(dado: object, campo: str) -> PassoLLM:
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, _CHAVES_PASSO, campo)
    tem_tool = "tool" in d
    tem_texto = "texto" in d
    if tem_tool == tem_texto:
        raise ErroFormato(campo, "deve ter exatamente um de 'tool' ou 'texto'")
    if tem_texto:
        if "args" in d or "pular" in d:
            raise ErroFormato(campo, "'args'/'pular' só valem com 'tool'")
        return PassoLLM(texto=_exigir_texto(d["texto"], f"{campo}.texto"))
    tool = _exigir_texto(d["tool"], f"{campo}.tool")
    args = d.get("args", {})
    if not isinstance(args, dict):
        raise ErroFormato(f"{campo}.args", "deve ser um mapeamento")
    pular = d.get("pular", False)
    if not isinstance(pular, bool):
        raise ErroFormato(f"{campo}.pular", "deve ser true/false")
    return PassoLLM(tool=tool, args=dict(args), pular=pular)


def _validar_espera(dado: object, campo: str) -> Mapping[str, Any]:
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, CHAVES_ESPERA, campo)
    if not d:
        raise ErroFormato(campo, "um turno precisa esperar algo observável")
    for chave in ("chama", "nao_chama", "contem", "nao_contem"):
        if chave in d:
            lista = _exigir_lista(d[chave], f"{campo}.{chave}")
            for i, item in enumerate(lista):
                _exigir_texto(item, f"{campo}.{chave}[{i}]")
    for chave in ("transfere", "proposta_no_estado", "bloqueado"):
        if chave in d and not isinstance(d[chave], bool):
            raise ErroFormato(f"{campo}.{chave}", "deve ser true/false")
    if "bolhas" in d and (isinstance(d["bolhas"], bool) or not isinstance(d["bolhas"], int)):
        raise ErroFormato(f"{campo}.bolhas", "deve ser inteiro")
    if "gateway_recebeu" in d:
        g = _exigir_dict(d["gateway_recebeu"], f"{campo}.gateway_recebeu")
        _exigir_texto(g.get("endpoint"), f"{campo}.gateway_recebeu.endpoint")
    if "classificacao" in d:
        c = _exigir_dict(d["classificacao"], f"{campo}.classificacao")
        _rejeitar_chaves(c, frozenset({"categoria", "origem"}), f"{campo}.classificacao")
    chamadas = set(d.get("chama", ())) & set(d.get("nao_chama", ()))
    if chamadas:
        raise ErroFormato(f"{campo}.nao_chama", f"'{sorted(chamadas)[0]}' também está em 'chama'")
    return dict(d)


def _validar_turno(dado: object, campo: str) -> Turno:
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, _CHAVES_TURNO, campo)
    cliente = _exigir_texto(d.get("cliente"), f"{campo}.cliente")
    passos = [
        _validar_passo(p, f"{campo}.llm[{i}]")
        for i, p in enumerate(_exigir_lista(d.get("llm"), f"{campo}.llm"))
    ]
    if passos[-1].texto is None:
        raise ErroFormato(
            f"{campo}.llm[{len(passos) - 1}]", "o último passo do LLM deve ser 'texto'"
        )
    return Turno(
        cliente=cliente,
        llm=tuple(passos),
        espera=_validar_espera(d.get("espera"), f"{campo}.espera"),
    )


def validar_cenario(
    dado: object,
    campo: str = "cenario",
    categorias: frozenset[str] | None = None,
    arquivo: Path | None = None,
) -> CenarioConversa:
    """Valida um cenário de conversa (formato de docs/plano/04)."""
    d = _exigir_dict(dado, campo)
    _rejeitar_chaves(d, _CHAVES_CENARIO, campo)
    id_ = _exigir_texto(d.get("id"), f"{campo}.id")
    descricao = _exigir_texto(d.get("descricao"), f"{campo}.descricao")
    s = _exigir_dict(d.get("sessao"), f"{campo}.sessao")
    _rejeitar_chaves(s, _CHAVES_SESSAO, f"{campo}.sessao")
    telefone = _exigir_texto(s.get("telefone"), f"{campo}.sessao.telefone")
    nome = s.get("nome_contato")
    if nome is not None:
        nome = _exigir_texto(nome, f"{campo}.sessao.nome_contato")
    contato = s.get("contato")
    if contato is not None:
        contato = _exigir_texto(contato, f"{campo}.sessao.contato")
    jev = [
        validar_resposta_jev(r, f"{campo}.jev[{i}]", categorias)
        for i, r in enumerate(_exigir_lista(d.get("jev"), f"{campo}.jev"))
    ]
    turnos = [
        _validar_turno(t, f"{campo}.turnos[{i}]")
        for i, t in enumerate(_exigir_lista(d.get("turnos"), f"{campo}.turnos"))
    ]
    if len(jev) != len(turnos):
        raise ErroFormato(
            f"{campo}.jev",
            f"{len(jev)} resposta(s) do JEV para {len(turnos)} turno(s): deve ser uma por turno",
        )
    xfail = d.get("xfail")
    if xfail is not None:
        xfail = _exigir_texto(xfail, f"{campo}.xfail")
    return CenarioConversa(
        id=id_,
        descricao=descricao,
        sessao=Sessao(telefone=telefone, nome_contato=nome, contato=contato),
        jev=tuple(jev),
        turnos=tuple(turnos),
        arquivo=arquivo,
        xfail=xfail,
    )


def carregar_cenario(caminho: Path, categorias: frozenset[str] | None = None) -> CenarioConversa:
    """Lê um YAML de cenário e valida; `arquivo` fica no objeto para mensagens."""
    with caminho.open(encoding="utf-8") as f:
        bruto = yaml.safe_load(f)
    return validar_cenario(bruto, campo=caminho.name, categorias=categorias, arquivo=caminho)
