# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Runner dos cenários de conversa (E3.5): executa cada YAML contra o grafo REAL.

Monta `build_graph(llm=<LLM scriptado>, tools=get_default_tools(),
buscar_chunks=<retriever por turno>, classificador=<ServicoClassificacao com FakeJEV>)` e
`httpx.post` das tools apontado para um gateway falso que responde com os JSON
de `contratos/tools-infodesk/` (o `ok.json`, ou o `erro-*`/falha de transporte que o
turno pedir em `gateway:`). O LLM e o JEV são reescritos A CADA TURNO com o script
daquele turno — um turno barrado antes do LLM (jailbreak na regex) não desalinha os
turnos seguintes.

Semântica das esperas (`CHAVES_ESPERA`), todas sobre o que o turno produziu:

- `chama` / `nao_chama`: a tool **executou** (uma recusa da política não conta);
- `transfere`: `acoes` do turno tem `transferir` OU `transferir_para_atendente`
  executou;
- `contem` / `nao_contem`: texto final do turno (sem distinção de caixa);
- `bolhas`: partes após `DividirBolhas` do gateway
  (`backend/internal/engine/whatsapp.go`): só uma LINHA com apenas `---` separa;
- `proposta_no_estado`: `proposta_chamado` preenchida ao fim do turno;
- `bloqueado`: o turno terminou em `safe_response` (`RESPOSTA_OFF_TOPIC`);
- `gateway_recebeu`: o gateway falso recebeu `POST <endpoint>` neste turno
  (`chave_idempotencia: "*"` exige a chave preenchida);
- `classificacao`: `categoria`/`origem` do estado ao fim do turno.

Além das chaves do formato base, os cenários usam as extensões de
`evals/conversa/extensoes.py` (`rag` e `gateway` por turno, `relogio`,
`avanca_minutos` e as esperas `prompt_contem`, `llm_chamadas`, `gateway_json`,
`gateway_nao_recebeu`, `gateway_vezes`, `estado`, `estado_nao_contem`).

Persona (CLAUDE.md, regra 10): TODA bolha de resposta final de TODO cenário passa
por `verificar_persona` (≤ 1 emoji, sem tratamento corporativo, sem prometer prazo),
inclusive as respostas fixas do código (`RESPOSTA_OFF_TOPIC` etc.).

`sessao.contato` aceita, além do id, os atalhos `telefone`, `cpf` e `contrato`:
o gateway já identificou o cliente por essa forma (Contrato A, `contato.forma`).
"""

from __future__ import annotations

import json
import os
import re
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Final
from unittest.mock import patch

from evals.conversa.extensoes import (
    GATEWAY_PREFIXO,
    PADRAO_RELOGIO,
    RAIZ_CONTRATOS,
    CenarioEval,
    ExtrasTurno,
    Observado,
    carregar_cenario_eval,
    verificar_extras,
)
from evals.formatos import CenarioConversa, Taxonomia, Turno, carregar_taxonomia

DIRETORIO: Final[Path] = Path(__file__).resolve().parent
RAIZ_AGENTE_IA: Final[Path] = DIRETORIO.parent.parent
GATEWAY_URL: Final[str] = "http://gateway.eval"
#: Endpoint do gateway (`gateway_recebeu.endpoint`) → pasta do contrato com o `ok.json`.
CAMINHOS_OK: Final[tuple[str, ...]] = (
    "contato/identificar",
    "ticket/preparar",
    "ticket/abrir",
    "ticket/consultar",
    "ticket/listar",
    "conversa/transferir",
    "cadastro/preparar",
    "cadastro/cadastrar",
)
FORMAS_ATALHO: Final[Mapping[str, str]] = {
    "telefone": "+55 31 9****-0000",
    "cpf": "***.***.789-**",
    "contrato": "****1234",
}


@dataclass(frozen=True)
class ResultadoCenario:
    """Saída de `executar`: `falhas` vazia significa que todas as esperas passaram."""

    cenario_id: str
    falhas: tuple[str, ...] = field(default_factory=tuple)
    turnos_executados: int = 0

    @property
    def passou(self) -> bool:
        return not self.falhas


@dataclass
class _Turno:
    """O que um turno produziu (insumo das esperas)."""

    texto: str = ""
    executadas: list[str] = field(default_factory=list)
    recusadas: list[str] = field(default_factory=list)
    acoes: list[dict[str, Any]] = field(default_factory=list)
    estado: dict[str, Any] = field(default_factory=dict)
    chamadas_gateway: list[dict[str, Any]] = field(default_factory=list)
    bloqueado: bool = False
    prompt_sistema: str = ""
    llm_chamadas: int = 0
    tool_textos: str = ""


def arquivos_de_cenario(diretorio: Path = DIRETORIO) -> list[Path]:
    """Todos os `*.yaml` do diretório (inclui o TEMPLATE, que deve ser válido), ordenados."""
    return sorted(p for p in diretorio.glob("*.yaml") if p.is_file())


def carregar_cenarios(
    diretorio: Path = DIRETORIO, taxonomia: Taxonomia | None = None
) -> list[CenarioConversa]:
    """Carrega e valida todos os cenários; ids têm de ser únicos."""
    if taxonomia is None:
        taxonomia = carregar_taxonomia()
    cenarios: list[CenarioConversa] = [
        carregar_cenario_eval(p, taxonomia.categorias) for p in arquivos_de_cenario(diretorio)
    ]
    vistos: dict[str, Path] = {}
    for c in cenarios:
        assert c.arquivo is not None
        if c.id in vistos:
            raise ValueError(f"{c.arquivo.name}: id '{c.id}' repetido (já em {vistos[c.id].name})")
        vistos[c.id] = c.arquivo
    return cenarios


def dividir_bolhas(texto: str) -> list[str]:
    """Espelho de `DividirBolhas` (Go): uma linha só com `---` separa; partes vazias somem."""
    partes: list[list[str]] = [[]]
    for linha in texto.splitlines():
        if linha.strip() == "---":
            partes.append([])
        else:
            partes[-1].append(linha)
    return [p for p in ("\n".join(x).strip() for x in partes) if p]


def _contato_da_sessao(valor: str | None) -> dict[str, Any] | None:
    if valor is None:
        return None
    if valor in FORMAS_ATALHO:
        return {"forma": valor, "valor_mascarado": FORMAS_ATALHO[valor], "cliente_id": "cli-eval"}
    return {"forma": "telefone", "valor_mascarado": FORMAS_ATALHO["telefone"], "cliente_id": valor}


class _GatewayFalso:
    """`httpx.post` das tools: responde `ok.json` do contrato e registra o que recebeu.

    `sobrescritas` (por turno, vem do YAML) troca a resposta de um caminho por outro
    arquivo do contrato (`erro-*`) ou por uma falha de transporte (`http_500`, `timeout`).
    Erros de negócio do gateway chegam como HTTP 200 `{status: false, motivo}` (contrato).
    """

    def __init__(self) -> None:
        self._ok: dict[str, Any] = {c: self._arquivo(c, "ok") for c in CAMINHOS_OK}
        self.chamadas: list[dict[str, Any]] = []
        self.sobrescritas: Mapping[str, str] = {}

    @staticmethod
    def _arquivo(caminho: str, nome: str) -> Any:
        return json.loads((RAIZ_CONTRATOS / caminho / f"{nome}.json").read_text(encoding="utf-8"))

    def post(self, url: str, *, json: Any = None, headers: Any = None, timeout: Any = None) -> Any:
        import httpx

        prefixo = GATEWAY_URL + GATEWAY_PREFIXO
        assert url.startswith(prefixo), f"URL fora do gateway: {url}"
        caminho = url.removeprefix(prefixo)
        self.chamadas.append({"endpoint": GATEWAY_PREFIXO + caminho, "json": json})
        if caminho not in self._ok:
            raise AssertionError(f"chamada inesperada ao gateway: {caminho}")
        pedido = httpx.Request("POST", url)
        troca = self.sobrescritas.get(caminho)
        if troca == "timeout":
            raise httpx.ReadTimeout("gateway eval: timeout simulado", request=pedido)
        if troca == "http_500":
            return httpx.Response(500, text="erro interno do gateway", request=pedido)
        corpo = self._arquivo(caminho, troca) if troca else self._ok[caminho]
        return httpx.Response(200, json=corpo, request=pedido)


class _Relogio:
    """ "Agora" do cenário: o carimbo/validade da proposta e o período do dia leem daqui."""

    def __init__(self, inicio: datetime) -> None:
        self.agora = inicio

    def avancar(self, minutos: float) -> None:
        self.agora += timedelta(minutes=minutos)


class _Retriever:
    """Retriever do nó `retrieve`: devolve os chunks do turno corrente (vazio por default)."""

    def __init__(self) -> None:
        self.chunks: tuple[Mapping[str, Any], ...] = ()

    def __call__(self, pergunta: str) -> dict[str, Any]:
        chunks = [dict(c) for c in self.chunks]
        return {"encontrado": bool(chunks), "total": len(chunks), "chunks": chunks}


@contextmanager
def _ambiente(gateway: _GatewayFalso, relogio: _Relogio) -> Iterator[None]:
    """Troca `httpx.post`, as configs das tools do Infodesk e o relógio (desfeito ao sair)."""
    from src.agent import nodes
    from src.config import get_settings
    from src.tools import infodesk_tools, proposta

    cfg = SimpleNamespace(
        infodesk_tools_base_url=GATEWAY_URL,
        infodesk_tools_token="token-eval",
        infodesk_tools_timeout=30.0,
    )
    # `get_settings()` exige a chave do OpenRouter para validar; nunca é usada (LLM falso).
    chave = (
        {} if os.environ.get("OPENROUTER_API_KEY") else {"OPENROUTER_API_KEY": "chave-falsa-evals"}
    )

    class _DatetimeCongelado(datetime):
        """`datetime` cujo `now()` é o relógio do cenário (período do dia da saudação)."""

        @classmethod
        def now(cls, tz: Any = None) -> Any:
            return relogio.agora.astimezone(tz) if tz else relogio.agora

    get_settings.cache_clear()
    try:
        with (
            patch.dict(os.environ, chave),
            patch.object(infodesk_tools.httpx, "post", gateway.post),
            patch.object(infodesk_tools, "get_settings", lambda: cfg),
            patch.object(proposta, "relogio", lambda: relogio.agora.astimezone(UTC)),
            patch.object(nodes, "datetime", _DatetimeCongelado),
        ):
            yield
    finally:
        get_settings.cache_clear()  # não deixa o Settings com a chave falsa para outros testes


def _resposta_jev(r: Any) -> Any:
    from tests.fakes import resp_jev

    return resp_jev(
        intencao=r.intencao,
        categoria=r.categoria,
        prioridade=r.prioridade,
        frustracao=r.frustracao,
        malicioso=r.malicioso,
        fora_de_escopo=r.fora_de_escopo,
    )


def _script_llm(turno: Turno, indice: int, extras: ExtrasTurno) -> list[Any]:
    """`AIMessage`s do turno: uma por tool (a menos de `pular`) e o texto final.

    Um passo `paralelo:` emite todas as suas tool calls na MESMA mensagem."""
    from langchain_core.messages import AIMessage

    saida: list[Any] = []
    for n, passo in enumerate(turno.llm):
        if passo.texto is not None:
            saida.append(AIMessage(content=passo.texto))
        elif n in extras.paralelas:
            chamadas = [
                {"id": f"t{indice}-{n}-{k}", "name": nome, "args": dict(args)}
                for k, (nome, args) in enumerate(extras.paralelas[n])
            ]
            saida.append(AIMessage(content="", tool_calls=chamadas))
        elif not passo.pular:
            assert passo.tool is not None
            chamada = {"id": f"t{indice}-{n}", "name": passo.tool, "args": dict(passo.args)}
            saida.append(AIMessage(content="", tool_calls=[chamada]))
    return saida


def _colher_turno(saida: Mapping[str, Any]) -> _Turno:
    """Lê as mensagens produzidas depois da última fala do cliente."""
    from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

    from src.agent.prompts import RESPOSTA_OFF_TOPIC

    mensagens = list(saida.get("messages") or [])
    inicio = 0
    for i in range(len(mensagens) - 1, -1, -1):
        if isinstance(mensagens[i], HumanMessage):
            inicio = i + 1
            break
    t = _Turno(estado=dict(saida), acoes=list(saida.get("acoes") or []))
    for m in mensagens[inicio:]:
        if isinstance(m, ToolMessage):
            t.tool_textos += str(m.content) + "\n"
            recusada = False
            try:
                corpo = json.loads(str(m.content))
                recusada = isinstance(corpo, dict) and bool(corpo.get("recusado_pela_politica"))
            except ValueError:
                pass
            (t.recusadas if recusada else t.executadas).append(str(m.name))
    ultimas = [m for m in mensagens[inicio:] if isinstance(m, AIMessage)]
    if ultimas:
        conteudo = ultimas[-1].content
        t.texto = (
            conteudo if isinstance(conteudo, str) else json.dumps(conteudo, ensure_ascii=False)
        )
    t.bloqueado = t.texto == RESPOSTA_OFF_TOPIC
    return t


#: Emoji = pictogramas e símbolos (a persona aceita no máximo UM por mensagem).
_EMOJI = re.compile("[\U0001f300-\U0001faff\u2600-\u27bf\u2b50\u2b55]")
#: Promessas que a Sofia nunca faz (prazo, retorno): `docs/plano/00` D7 e system prompt.
_PROMESSAS = re.compile(
    r"\bem at[eé] \d|\bdentro d[eo] (?:\d|prazo)|\bprazo (?:de|é|e) |"
    r"\bpr[oó]ximas? \d+ (?:horas|dias)\b|\bem \d+ ?(?:min|h\b|hora|dia)|\bamanh[ãa]\b|"
    r"\bainda hoje\b|\bem instantes\b|\blogo (?:te|retorn)|\bem breve\b",
    re.IGNORECASE,
)
_CORPORATIVO = re.compile(
    r"\b(?:prezad[oa]s?|sua solicita[cç][aã]o|conforme alinhado)\b", re.IGNORECASE
)


def verificar_persona(texto: str, rotulo: str) -> list[str]:
    """Persona Sofia sobre uma resposta final (regra 10 do CLAUDE.md), bolha a bolha."""
    falhas: list[str] = []
    for n, bolha in enumerate(dividir_bolhas(texto), start=1):
        onde = f"{rotulo} bolha {n}"
        if len(_EMOJI.findall(bolha)) > 1:
            falhas.append(f"{onde}: persona — mais de um emoji")
        if (m := _PROMESSAS.search(bolha)) is not None:
            falhas.append(f"{onde}: persona — promete prazo/retorno ({m.group(0)!r})")
        if (m := _CORPORATIVO.search(bolha)) is not None:
            falhas.append(f"{onde}: persona — linguagem corporativa ({m.group(0)!r})")
        letras = [c for c in bolha if c.isalpha()]
        if len(letras) >= 12 and sum(c.isupper() for c in letras) / len(letras) > 0.6:
            falhas.append(f"{onde}: persona — CAPS LOCK")
    return falhas


def _verificar(espera: Mapping[str, Any], t: _Turno, rotulo: str) -> list[str]:
    falhas: list[str] = verificar_persona(t.texto, rotulo)
    baixo = t.texto.lower()
    for nome in espera.get("chama", ()):
        if nome not in t.executadas:
            quando = "foi recusada pela política" if nome in t.recusadas else "não foi chamada"
            falhas.append(f"{rotulo}: chama '{nome}' — {quando} (executadas: {t.executadas})")
    for nome in espera.get("nao_chama", ()):
        if nome in t.executadas:
            falhas.append(f"{rotulo}: nao_chama '{nome}' — a tool executou")
    if "transfere" in espera:
        transferiu = any(a.get("tipo") == "transferir" for a in t.acoes) or (
            "transferir_para_atendente" in t.executadas
        )
        if transferiu != espera["transfere"]:
            falhas.append(
                f"{rotulo}: transfere={espera['transfere']} mas transferiu={transferiu} "
                f"(acoes={[a.get('origem') for a in t.acoes]})"
            )
    for trecho in espera.get("contem", ()):
        if str(trecho).lower() not in baixo:
            falhas.append(f"{rotulo}: contem {trecho!r} — texto: {t.texto!r}")
    for trecho in espera.get("nao_contem", ()):
        if str(trecho).lower() in baixo:
            falhas.append(f"{rotulo}: nao_contem {trecho!r} — texto: {t.texto!r}")
    if "bolhas" in espera and len(dividir_bolhas(t.texto)) != espera["bolhas"]:
        falhas.append(
            f"{rotulo}: bolhas={espera['bolhas']} mas saíram {len(dividir_bolhas(t.texto))}"
        )
    if "proposta_no_estado" in espera:
        tem = bool(t.estado.get("proposta_chamado"))
        if tem != espera["proposta_no_estado"]:
            falhas.append(
                f"{rotulo}: proposta_no_estado={espera['proposta_no_estado']} mas tem={tem}"
            )
    if "bloqueado" in espera and t.bloqueado != espera["bloqueado"]:
        falhas.append(f"{rotulo}: bloqueado={espera['bloqueado']} mas bloqueado={t.bloqueado}")
    if "gateway_recebeu" in espera:
        g = espera["gateway_recebeu"]
        casadas = [c for c in t.chamadas_gateway if c["endpoint"] == g["endpoint"]]
        if not casadas:
            vistos = [c["endpoint"] for c in t.chamadas_gateway]
            falhas.append(f"{rotulo}: gateway_recebeu {g['endpoint']} — recebeu {vistos}")
        elif g.get("chave_idempotencia") == "*" and not any(
            (c["json"] or {}).get("chave_idempotencia") for c in casadas
        ):
            falhas.append(f"{rotulo}: gateway_recebeu {g['endpoint']} sem chave_idempotencia")
    falhas.extend(
        verificar_extras(
            espera,
            Observado(
                prompt_sistema=t.prompt_sistema,
                tool_textos=t.tool_textos,
                llm_chamadas=t.llm_chamadas,
                chamadas_gateway=t.chamadas_gateway,
                estado=t.estado,
            ),
            rotulo,
        )
    )
    if "classificacao" in espera:
        atual = t.estado.get("classificacao") or {}
        for chave, valor in espera["classificacao"].items():
            if atual.get(chave) != valor:
                falhas.append(
                    f"{rotulo}: classificacao.{chave}={atual.get(chave)!r}, esperado {valor!r}"
                )
    return falhas


def executar(cenario: CenarioConversa) -> ResultadoCenario:
    """Executa um cenário contra o grafo real (LLM, JEV e gateway falsos)."""
    from langchain_core.messages import HumanMessage

    from src.agent.graph import build_graph, get_default_tools
    from tests.fakes import FakeJEV, _texto_de, fake_llm, servico_fake

    jev = FakeJEV()
    llm = fake_llm()
    gateway = _GatewayFalso()
    relogio = _Relogio(PADRAO_RELOGIO)
    retriever = _Retriever()
    if isinstance(cenario, CenarioEval):
        relogio = _Relogio(cenario.extras.relogio)
    thread = f"conv-{cenario.id}"
    config = {"configurable": {"thread_id": thread}}
    falhas: list[str] = []
    executados = 0
    with _ambiente(gateway, relogio):
        grafo = build_graph(
            llm=llm,
            tools=get_default_tools(),
            buscar_chunks=retriever,
            classificador=servico_fake(jev),
        )
        for i, turno in enumerate(cenario.turnos, start=1):
            rotulo = f"turno {i}"
            extras = (
                cenario.extras.turnos[i - 1] if isinstance(cenario, CenarioEval) else ExtrasTurno()
            )
            relogio.avancar(extras.avanca_minutos)
            retriever.chunks = extras.rag
            gateway.sobrescritas = extras.gateway
            jev._padrao = _resposta_jev(cenario.jev[i - 1])
            script = _script_llm(turno, i, extras)
            llm.invoke.side_effect = script
            entrada: dict[str, Any] = {"messages": [HumanMessage(content=turno.cliente)]}
            if i == 1:
                entrada.update(
                    telefone=cenario.sessao.telefone,
                    conversation_id=thread,
                    nome_contato=cenario.sessao.nome_contato,
                    contato=_contato_da_sessao(cenario.sessao.contato),
                )
            antes = len(gateway.chamadas)
            llm_antes = len(llm.invoke.call_args_list)
            try:
                saida = grafo.invoke(entrada, config=config)
            except StopIteration:
                falhas.append(f"{rotulo}: o grafo chamou o LLM mais vezes que o script prevê")
                break
            except Exception as exc:
                falhas.append(f"{rotulo}: o grafo levantou {type(exc).__name__}: {exc}")
                break
            executados += 1
            colhido = _colher_turno(saida)
            colhido.chamadas_gateway = gateway.chamadas[antes:]
            chamadas_llm = llm.invoke.call_args_list[llm_antes:]
            colhido.llm_chamadas = len(chamadas_llm)
            if not colhido.bloqueado and len(chamadas_llm) < len(script):
                falhas.append(
                    f"{rotulo}: o script do LLM tem {len(script)} passo(s) mas o grafo o chamou "
                    f"{len(chamadas_llm)}x — passos sobrando passam em silêncio"
                )
            colhido.prompt_sistema = "\n".join(
                dict.fromkeys(_texto_de(c.args[0][0].content) for c in chamadas_llm)
            )
            falhas.extend(_verificar({**turno.espera, **extras.espera}, colhido, rotulo))
    return ResultadoCenario(cenario.id, tuple(falhas), executados)


def resumo(cenarios: Sequence[CenarioConversa], resultados: Mapping[str, ResultadoCenario]) -> str:
    """Tabela dos cenários com o resultado da execução (e o motivo do xfail, se houver)."""
    linhas = [f"{len(cenarios)} cenário(s) em {DIRETORIO}:"]
    for c in sorted(cenarios, key=lambda c: c.id):
        r = resultados.get(c.id)
        estado = "?" if r is None else ("ok" if r.passou else f"{len(r.falhas)} falha(s)")
        marca = " [xfail]" if c.xfail else ""
        linhas.append(
            f"  {c.id:<18} {len(c.turnos)} turno(s)  {estado:<10}{marca}  — {c.descricao}"
        )
    return "\n".join(linhas)


def relatorio_markdown(
    cenarios: Sequence[CenarioConversa], resultados: Mapping[str, ResultadoCenario]
) -> str:
    """Relatório curto em Markdown: uma linha por cenário (arquivo = o comportamento provado)."""
    normais = [c for c in cenarios if not c.xfail]
    passaram = sum(1 for c in normais if (r := resultados.get(c.id)) is not None and r.passou)
    xfails = [c for c in cenarios if c.xfail]
    linhas = [
        "# Evals de conversa (determinísticos)",
        "",
        f"- cenários: {len(cenarios)} ({passaram}/{len(normais)} normais passaram, "
        f"{len(xfails)} xfail documentando bug de produto)",
        "",
        "| id | comportamento | turnos | resultado |",
        "|---|---|---|---|",
    ]
    for c in sorted(cenarios, key=lambda c: c.id):
        r = resultados.get(c.id)
        if r is None:
            estado = "não executado"
        elif c.xfail:
            estado = "xfail (esperado)" if not r.passou else "PASSOU — remova o xfail"
        else:
            estado = "ok" if r.passou else f"FALHOU ({len(r.falhas)})"
        nome = c.arquivo.stem if c.arquivo is not None else c.id
        linhas.append(f"| {c.id} | {nome} | {len(c.turnos)} | {estado} |")
    for c in xfails:
        linhas += ["", f"**{c.id}** xfail: {c.xfail}"]
    return "\n".join(linhas) + "\n"
