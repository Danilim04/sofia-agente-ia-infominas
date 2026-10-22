# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Runner dos cenários de conversa (E3.5): executa cada YAML contra o grafo REAL.

Monta `build_graph(llm=<LLM scriptado>, tools=get_default_tools(),
buscar_chunks=sem_rag, classificador=<ServicoClassificacao com FakeJEV>)` e
`httpx.post` das tools apontado para um gateway falso que responde com os JSON
de `contratos/tools-infodesk/` (sempre o `ok.json`: erros do gateway não são
scriptáveis em YAML, TODO(E3.5): `gateway:` por turno). O LLM e o JEV são
reescritos A CADA TURNO com o script daquele turno — um turno barrado antes do
LLM (jailbreak na regex) não desalinha os turnos seguintes.

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

`sessao.contato` aceita, além do id, os atalhos `telefone`, `cpf` e `contrato`:
o gateway já identificou o cliente por essa forma (Contrato A, `contato.forma`).
"""

from __future__ import annotations

import json
import os
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Final
from unittest.mock import patch

from evals.formatos import CenarioConversa, Taxonomia, Turno, carregar_cenario, carregar_taxonomia

DIRETORIO: Final[Path] = Path(__file__).resolve().parent
RAIZ_AGENTE_IA: Final[Path] = DIRETORIO.parent.parent
GATEWAY_URL: Final[str] = "http://gateway.eval"
GATEWAY_PREFIXO: Final[str] = "/tools/infodesk/"
#: Endpoint do gateway (`gateway_recebeu.endpoint`) → pasta do contrato com o `ok.json`.
CAMINHOS_OK: Final[tuple[str, ...]] = (
    "contato/identificar",
    "ticket/preparar",
    "ticket/abrir",
    "ticket/consultar",
    "ticket/listar",
    "conversa/transferir",
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


def arquivos_de_cenario(diretorio: Path = DIRETORIO) -> list[Path]:
    """Todos os `*.yaml` do diretório (inclui o TEMPLATE, que deve ser válido), ordenados."""
    return sorted(p for p in diretorio.glob("*.yaml") if p.is_file())


def carregar_cenarios(
    diretorio: Path = DIRETORIO, taxonomia: Taxonomia | None = None
) -> list[CenarioConversa]:
    """Carrega e valida todos os cenários; ids têm de ser únicos."""
    if taxonomia is None:
        taxonomia = carregar_taxonomia()
    cenarios = [carregar_cenario(p, taxonomia.categorias) for p in arquivos_de_cenario(diretorio)]
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
    """`httpx.post` das tools: responde `ok.json` do contrato e registra o que recebeu."""

    def __init__(self) -> None:
        raiz = RAIZ_AGENTE_IA.parent / "contratos" / "tools-infodesk"
        self._ok: dict[str, Any] = {
            c: json.loads((raiz / c / "ok.json").read_text(encoding="utf-8")) for c in CAMINHOS_OK
        }
        self.chamadas: list[dict[str, Any]] = []

    def post(self, url: str, *, json: Any = None, headers: Any = None, timeout: Any = None) -> Any:
        import httpx

        prefixo = GATEWAY_URL + GATEWAY_PREFIXO
        assert url.startswith(prefixo), f"URL fora do gateway: {url}"
        caminho = url.removeprefix(prefixo)
        self.chamadas.append({"endpoint": GATEWAY_PREFIXO + caminho, "json": json})
        if caminho not in self._ok:
            raise AssertionError(f"chamada inesperada ao gateway: {caminho}")
        return httpx.Response(200, json=self._ok[caminho], request=httpx.Request("POST", url))


@contextmanager
def _ambiente(gateway: _GatewayFalso) -> Iterator[None]:
    """Troca `httpx.post` e as configurações das tools do Infodesk (desfeito ao sair)."""
    from src.config import get_settings
    from src.tools import infodesk_tools

    cfg = SimpleNamespace(
        infodesk_tools_base_url=GATEWAY_URL,
        infodesk_tools_token="token-eval",
        infodesk_tools_timeout=30.0,
    )
    # `get_settings()` exige a chave do OpenRouter para validar; nunca é usada (LLM falso).
    chave = (
        {} if os.environ.get("OPENROUTER_API_KEY") else {"OPENROUTER_API_KEY": "chave-falsa-evals"}
    )
    get_settings.cache_clear()
    with (
        patch.dict(os.environ, chave),
        patch.object(infodesk_tools.httpx, "post", gateway.post),
        patch.object(infodesk_tools, "get_settings", lambda: cfg),
    ):
        yield


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


def _script_llm(turno: Turno, indice: int) -> list[Any]:
    """`AIMessage`s do turno: uma por tool (a menos de `pular`) e o texto final."""
    from langchain_core.messages import AIMessage

    saida: list[Any] = []
    for n, passo in enumerate(turno.llm):
        if passo.texto is not None:
            saida.append(AIMessage(content=passo.texto))
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


def _verificar(espera: Mapping[str, Any], t: _Turno, rotulo: str) -> list[str]:
    falhas: list[str] = []
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
    from tests.fakes import FakeJEV, fake_llm, sem_rag, servico_fake

    jev = FakeJEV()
    llm = fake_llm()
    gateway = _GatewayFalso()
    thread = f"conv-{cenario.id}"
    config = {"configurable": {"thread_id": thread}}
    falhas: list[str] = []
    executados = 0
    with _ambiente(gateway):
        grafo = build_graph(
            llm=llm,
            tools=get_default_tools(),
            buscar_chunks=sem_rag,
            classificador=servico_fake(jev),
        )
        for i, turno in enumerate(cenario.turnos, start=1):
            rotulo = f"turno {i}"
            jev._padrao = _resposta_jev(cenario.jev[i - 1])
            llm.invoke.side_effect = _script_llm(turno, i)
            entrada: dict[str, Any] = {"messages": [HumanMessage(content=turno.cliente)]}
            if i == 1:
                entrada.update(
                    telefone=cenario.sessao.telefone,
                    conversation_id=thread,
                    nome_contato=cenario.sessao.nome_contato,
                    contato=_contato_da_sessao(cenario.sessao.contato),
                )
            antes = len(gateway.chamadas)
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
            falhas.extend(_verificar(turno.espera, colhido, rotulo))
    return ResultadoCenario(cenario.id, tuple(falhas), executados)


def resumo(cenarios: Sequence[CenarioConversa], resultados: Mapping[str, ResultadoCenario]) -> str:
    """Tabela dos cenários com o resultado da execução (e o motivo do xfail, se houver)."""
    linhas = [f"{len(cenarios)} cenário(s) em {DIRETORIO}:"]
    for c in cenarios:
        r = resultados.get(c.id)
        estado = "?" if r is None else ("ok" if r.passou else f"{len(r.falhas)} falha(s)")
        marca = " [xfail]" if c.xfail else ""
        linhas.append(
            f"  {c.id:<18} {len(c.turnos)} turno(s)  {estado:<10}{marca}  — {c.descricao}"
        )
    return "\n".join(linhas)
