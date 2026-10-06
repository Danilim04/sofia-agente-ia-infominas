"""ZB-3 — reprodução da conversa #45: nenhum chamado é criado.

Efeito em produção (rodada 1): o bot abriu um chamado novo (ZPRS25208029) sem
consentimento real. Havia uma `proposta_chamado` de um episódio anterior no
estado da conversa (o Chatwoot reutiliza a conversa; o `thread_id` é o id
dela); o cliente citou um protocolo EXISTENTE ("ZPRS25208011") e respondeu
"Esse", e o modelo tratou isso como confirmação da proposta antiga.

FIXTURE RECONSTRUÍDA, NÃO COPIADA: a transcrição real da #45 não está no repo
e não há acesso a produção. A sequência segue o que o backlog descreve —
proposta pendente de um episódio anterior; cliente envia "ZPRS25208011";
depois "Esse" — com o modelo ROTEIRIZADO para repetir o comportamento do
incidente (insistir em `abrir_chamado_suporte`). O gateway é um stub que
imita o Go da branch (ZB-2: relator com chamado aberto → `chamado_aberto`).

As barreiras exercitadas (todas em código, nenhuma no modelo):
  1. ZB-1 conteúdo/turno: "ZPRS25208011" e "Esse" não são confirmação explícita;
     a confirmação tem de ser a mensagem SEGUINTE à proposta.
  2. ZB-1 validade: proposta de outro episódio (horas) venceu (15 min).
  3. ZB-1 resolução: conversa resolvida no Chatwoot → `/encerrar-episodio`
     limpa a proposta antes do novo episódio.
  4. ZB-2: protocolo citado nunca vira chamado novo (preparar recusado) e,
     mesmo que o modelo tentasse preparar, o gateway recusa com os abertos.
"""

from __future__ import annotations

import asyncio
from datetime import datetime, timedelta, timezone
from unittest.mock import MagicMock

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

import server
from src.agent import nodes
from src.agent.graph import build_graph
from src.agent.prompts import SYSTEM_PROMPT_AGENTE
from src.tools import sac_tools
from src.tools.sac_tools import SAC_TOOLS

CONVERSA = "45"
PROTOCOLO_EXISTENTE = "ZPRS25208011"  # citado pelo cliente — chamado real dele
PROTOCOLO_INDEVIDO = "ZPRS25208029"  # o que o bot abriu em produção

_IDENTIDADE = {
    "encontrado": True,
    "login": "10596693664",
    "nome": "Cliente",
    "empresas": [
        {"grupo_empresa": "MEARIM", "grupo_user": "COLABORADOR", "area": "OPERACAO", "bases": []}
    ],
}

_PROPOSTA_ANTIGA = {
    "resumo": "APP TRAVANDO AO BIPAR",
    "descricao": "O app trava ao bipar a nota",
    "categoria": "APLICATIVO",
    "ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
    "prioridade": "MEDIA",
    "empresa": "MEARIM",
    "prazo": 1,
    "novo_chamado": False,
}


class _GatewayComChamadoAberto:
    """Stub do gateway Go para a #45: o relator tem o ZPRS25208011 EM ABERTO.

    Imita o toolsapi da branch: /preparar sem `novo_chamado` → `chamado_aberto`
    com a lista; /criar devolve o protocolo indevido (se alguém chegar lá, o
    teste falha); /listar devolve o aberto.

    `relator_com_aberto` começa False: no episódio ANTERIOR a proposta foi
    preparada sem nada em aberto (é isso que deixa uma proposta pendente, como
    na #45); o ZPRS25208011 passa a constar entre os episódios (aberto por um
    atendente ou por outro canal) — na #45 o cliente o cita ao voltar.
    """

    def __init__(self):
        self.chamadas: list[tuple[str, dict]] = []
        self.relator_com_aberto = False

    def __call__(self, url, json=None, headers=None, timeout=None):
        rota = url.rsplit("/", 1)[-1]
        self.chamadas.append((rota, dict(json or {})))
        aberto = {
            "protocolo": PROTOCOLO_EXISTENTE,
            "resumo": "APP TRAVANDO",
            "status": "PENDENTE",
            "link": f"https://sac/chat/x/MEARIM/{PROTOCOLO_EXISTENTE}",
        }
        if rota == "preparar":
            if json.get("novo_chamado") or not self.relator_com_aberto:
                payload = {
                    "status": True,
                    "proposta": dict(_PROPOSTA_ANTIGA, novo_chamado=bool(json.get("novo_chamado"))),
                }
            else:
                payload = {
                    "status": False,
                    "motivo": "chamado_aberto",
                    "total": 1,
                    "chamados_abertos": [aberto],
                    "erro": "o cliente já tem chamado(s) em aberto",
                }
        elif rota == "criar":
            payload = {"status": True, "protocolo": PROTOCOLO_INDEVIDO, "link": "https://sac/x"}
        else:
            abertos = [aberto] if self.relator_com_aberto else []
            payload = {"status": True, "total": len(abertos), "chamados": abertos}

        class _Resp:
            def raise_for_status(self):
                return None

            def json(self):
                return payload

        return _Resp()

    def rotas(self) -> list[str]:
        return [r for r, _ in self.chamadas]


@pytest.fixture
def gateway(monkeypatch) -> _GatewayComChamadoAberto:
    """Gateway do episódio do incidente: o relator JÁ tem o ZPRS25208011 aberto.
    Os testes que rodam o episódio anterior desligam a flag antes dele."""
    gw = _GatewayComChamadoAberto()
    gw.relator_com_aberto = True
    monkeypatch.setattr(sac_tools.httpx, "post", gw)
    monkeypatch.setattr(
        nodes,
        "avaliar_entrada",
        lambda texto, **_: {"is_safe": True, "categoria": "suporte", "motivo": "t"},
    )
    return gw


def _llm(scripted: list[AIMessage]) -> MagicMock:
    fake = MagicMock(name="FakeLLM")
    fake.bind_tools.return_value = fake
    fake.invoke.side_effect = list(scripted)
    return fake


def _tc(nome: str, args: dict | None = None, id_: str = "tc") -> AIMessage:
    return AIMessage(content="", tool_calls=[{"id": id_, "name": nome, "args": args or {}}])


def _tc_preparar(novo_chamado: bool = False) -> AIMessage:
    return _tc(
        "preparar_abertura_chamado",
        {
            "resumo": "App travando ao bipar",
            "descricao": "O app trava ao bipar a nota",
            "categoria": "APLICATIVO",
            "ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
            **({"novo_chamado": True} if novo_chamado else {}),
        },
        id_="tc-prep",
    )


_CFG = {"configurable": {"thread_id": CONVERSA}}


def _ep1_proposta_pendente(g, gateway: _GatewayComChamadoAberto) -> dict:
    """Episódio anterior: cliente relata, modelo prepara e pergunta; o cliente
    NÃO confirma (a conversa seguiu/foi resolvida com a proposta pendente).
    Ainda não havia chamado em aberto; o ZPRS25208011 surge em seguida."""
    gateway.relator_com_aberto = False
    out = g.invoke(
        {
            "telefone": "5531999990000",
            "identidade": _IDENTIDADE,
            "conversation_id": CONVERSA,
            "messages": [HumanMessage(content="o app trava quando vou bipar a nota")],
        },
        config=_CFG,
    )
    gateway.relator_com_aberto = True
    return out


def _ep2_protocolo_e_esse(g) -> tuple[dict, dict]:
    """A sequência do incidente: "ZPRS25208011" e depois "Esse"."""
    out_a = g.invoke({"messages": [HumanMessage(content=PROTOCOLO_EXISTENTE)]}, config=_CFG)
    out_b = g.invoke({"messages": [HumanMessage(content="Esse")]}, config=_CFG)
    return out_a, out_b


def _tool_msgs(out: dict, nome: str) -> list[ToolMessage]:
    return [m for m in out["messages"] if isinstance(m, ToolMessage) and m.name == nome]


def _nenhum_chamado_criado(gateway: _GatewayComChamadoAberto, *outs: dict) -> None:
    assert "criar" not in gateway.rotas(), gateway.rotas()
    for out in outs:
        for m in out["messages"]:
            assert PROTOCOLO_INDEVIDO not in str(m.content)


# ---------------------------------------------------------------------------
# Barreira 1 — mesmo com a proposta "fresca" (pior caso), as mensagens do
# incidente não confirmam nada.
# ---------------------------------------------------------------------------


def test_conversa_45_proposta_pendente_protocolo_e_esse_nao_abrem_chamado(gateway):
    fake = _llm(
        [
            # Episódio anterior
            _tc_preparar(),
            AIMessage(content="Vou registrar: APP TRAVANDO AO BIPAR (prazo 1 dia). Posso abrir?"),
            # Episódio do incidente — o modelo repete o erro de produção: trata
            # o protocolo e o "Esse" como confirmação e tenta abrir.
            _tc("abrir_chamado_suporte", id_="tc-abrir-1"),
            AIMessage(content="Entendi, você está falando do seu chamado em aberto."),
            _tc("abrir_chamado_suporte", id_="tc-abrir-2"),
            AIMessage(content="Esse chamado continua em andamento: https://sac/chat/x/MEARIM/ZPRS25208011"),
        ]
    )
    g = build_graph(llm=fake, tools=SAC_TOOLS, buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []})

    out1 = _ep1_proposta_pendente(g, gateway)
    assert out1["proposta_chamado"] is not None  # proposta pendente, como na #45
    assert gateway.rotas() == ["preparar"]

    out_a, out_b = _ep2_protocolo_e_esse(g)

    # O que importa: nenhum /criar, nenhum ZPRS25208029 (no código antigo o
    # "ZPRS25208011" já abria o chamado — a política só olhava se havia proposta).
    _nenhum_chamado_criado(gateway, out1, out_a, out_b)
    recusa_a = _tool_msgs(out_a, "abrir_chamado_suporte")[-1].content
    recusa_b = _tool_msgs(out_b, "abrir_chamado_suporte")[-1].content
    assert "recusado_pela_politica" in recusa_a and "confirmação explícita" in recusa_a
    assert "recusado_pela_politica" in recusa_b and "SEGUINTE" in recusa_b
    assert out1["proposta_chamado"]["turno"] == 1
    assert out_b["proposta_chamado"] is not None  # nunca foi consumida


# ---------------------------------------------------------------------------
# Barreira 2 — o caso real: a proposta é de outro episódio, horas antes.
# ---------------------------------------------------------------------------


def test_conversa_45_proposta_de_outro_episodio_venceu(gateway):
    fake = _llm(
        [
            _tc("abrir_chamado_suporte", id_="tc-abrir-1"),
            AIMessage(content="Esse é o seu chamado em aberto."),
            _tc("abrir_chamado_suporte", id_="tc-abrir-2"),
            AIMessage(content="Ele segue em andamento."),
        ]
    )
    g = build_graph(llm=fake, tools=SAC_TOOLS, buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []})
    antiga = {
        **_PROPOSTA_ANTIGA,
        "criada_em": (datetime.now(timezone.utc) - timedelta(hours=5)).isoformat(),
        "turno": 1,
    }
    # Estado herdado do episódio anterior (histórico + proposta pendente).
    g.update_state(
        _CFG,
        {
            "telefone": "5531999990000",
            "identidade": _IDENTIDADE,
            "conversation_id": CONVERSA,
            "proposta_chamado": antiga,
            "messages": [
                HumanMessage(content="o app trava quando vou bipar a nota"),
                AIMessage(content="Vou registrar: APP TRAVANDO AO BIPAR. Posso abrir?"),
            ],
        },
        as_node="output_guardrail",
    )

    out_a, out_b = _ep2_protocolo_e_esse(g)

    for out in (out_a, out_b):
        recusa = _tool_msgs(out, "abrir_chamado_suporte")[-1].content
        assert "recusado_pela_politica" in recusa and "venceu" in recusa
    _nenhum_chamado_criado(gateway, out_a, out_b)


# ---------------------------------------------------------------------------
# Barreira 3 — a conversa foi RESOLVIDA entre os episódios: o gateway chama
# /encerrar-episodio e a proposta some antes de o cliente voltar.
# ---------------------------------------------------------------------------


def test_conversa_45_resolvida_entre_episodios_limpa_a_proposta(gateway):
    fake = _llm(
        [
            _tc_preparar(),
            AIMessage(content="Vou registrar: APP TRAVANDO AO BIPAR. Posso abrir?"),
            _tc("abrir_chamado_suporte", id_="tc-abrir-1"),
            AIMessage(content="Você está falando do chamado ZPRS25208011."),
            _tc("abrir_chamado_suporte", id_="tc-abrir-2"),
            AIMessage(content="Ele segue em andamento."),
        ]
    )
    g = build_graph(llm=fake, tools=SAC_TOOLS, buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []})

    out1 = _ep1_proposta_pendente(g, gateway)
    assert out1["proposta_chamado"] is not None

    # Atendente resolve a conversa no Chatwoot → gateway → POST /encerrar-episodio.
    resp = asyncio.run(server.encerrar_episodio(g, CONVERSA))
    assert resp.proposta_limpa is True

    out_a, out_b = _ep2_protocolo_e_esse(g)
    for out in (out_a, out_b):
        recusa = _tool_msgs(out, "abrir_chamado_suporte")[-1].content
        assert "recusado_pela_politica" in recusa and "não há proposta" in recusa
        assert out["proposta_chamado"] is None
    _nenhum_chamado_criado(gateway, out1, out_a, out_b)
    # O histórico da conversa sobreviveu à resolução (auditoria).
    assert out_b["messages"][0].content == "o app trava quando vou bipar a nota"


# ---------------------------------------------------------------------------
# Barreira 4 — ZB-2: protocolo citado nunca vira chamado novo; e o gateway, por
# sua vez, recusa preparar para relator com chamado aberto.
# ---------------------------------------------------------------------------


def test_conversa_45_protocolo_citado_nao_vira_chamado_novo(gateway):
    fake = _llm(
        [
            # O modelo tenta PREPARAR um chamado novo a partir do protocolo.
            _tc_preparar(),
            # Recusado pela política → ele lista, como a recusa manda.
            _tc("listar_chamados_abertos", id_="tc-listar"),
            AIMessage(
                content="Esse é o seu chamado ZPRS25208011 (APP TRAVANDO), em andamento: "
                "https://sac/chat/x/MEARIM/ZPRS25208011 — pode falar com o atendente por lá."
            ),
            # "Esse": o modelo tenta preparar de novo, agora inventando novo_chamado=true.
            _tc_preparar(novo_chamado=True),
            AIMessage(content="Certo, esse chamado segue em andamento pelo link acima."),
        ]
    )
    g = build_graph(llm=fake, tools=SAC_TOOLS, buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []})
    g.update_state(
        _CFG,
        {"telefone": "5531999990000", "identidade": _IDENTIDADE, "conversation_id": CONVERSA},
        as_node="output_guardrail",
    )

    out_a, out_b = _ep2_protocolo_e_esse(g)

    recusa_prep = _tool_msgs(out_a, "preparar_abertura_chamado")[-1].content
    assert "recusado_pela_politica" in recusa_prep and PROTOCOLO_EXISTENTE in recusa_prep
    assert "listar_chamados_abertos" in recusa_prep
    listagem = _tool_msgs(out_a, "listar_chamados_abertos")[-1].content
    assert PROTOCOLO_EXISTENTE in listagem

    recusa_novo = _tool_msgs(out_b, "preparar_abertura_chamado")[-1].content
    assert "recusado_pela_politica" in recusa_novo and "novo_chamado=true" in recusa_novo

    # Só a listagem chegou ao gateway: nenhum preparar, nenhum criar.
    assert gateway.rotas() == ["listar"]
    _nenhum_chamado_criado(gateway, out_a, out_b)
    assert out_b.get("proposta_chamado") is None


def test_conversa_45_gateway_recusa_preparar_com_chamado_aberto(gateway):
    """Defesa em profundidade: se um preparo legítimo chegar ao gateway (sem
    protocolo na mensagem), o Go devolve `chamado_aberto` com a lista e não
    há proposta para abrir."""
    fake = _llm(
        [
            _tc_preparar(),
            AIMessage(content="Você já tem o chamado ZPRS25208011 em aberto. É sobre ele?"),
        ]
    )
    g = build_graph(llm=fake, tools=SAC_TOOLS, buscar_chunks=lambda q: {"encontrado": False, "total": 0, "chunks": []})
    out = g.invoke(
        {
            "telefone": "5531999990000",
            "identidade": _IDENTIDADE,
            "conversation_id": CONVERSA,
            "messages": [HumanMessage(content="o app trava de novo, abre um chamado")],
        },
        config=_CFG,
    )
    resultado = _tool_msgs(out, "preparar_abertura_chamado")[-1].content
    assert "chamado_aberto" in resultado and PROTOCOLO_EXISTENTE in resultado
    assert out.get("proposta_chamado") is None  # nada foi guardado para abrir
    assert gateway.rotas() == ["preparar"]
    assert gateway.chamadas[0][1]["novo_chamado"] is False


# ---------------------------------------------------------------------------
# Prompt — as regras também estão escritas para o modelo (ZB-1/ZB-2).
# ---------------------------------------------------------------------------


def test_prompt_traz_as_regras_de_consentimento_e_protocolo():
    sp = SYSTEM_PROMPT_AGENTE
    assert "PROTOCOLO CITADO PELO CLIENTE" in sp and "NUNCA vira chamado novo" in sp
    assert "chamado_aberto" in sp and "novo_chamado=true" in sp
    assert "MENSAGEM SEGUINTE" in sp and "15 minutos" in sp
    assert '"esse"' in sp  # a resposta do incidente, nomeada como não-confirmação
    assert "sem_email" in sp and "sem_empresa" in sp
