"""Testes da camada Chainlit (harness de dev) — só helpers puros, sem rede.

Cobre o pedido do Contrato A montado a partir das entradas do Chainlit, a
renderização de `classificacao`/`acoes`, o telefone mascarado e a taxonomia de
dev (`scripts/dev/taxonomia.dev.yaml`) contra o `mock-infodesk/dados.json`.
Os handlers `@cl.on_chat_start` e `@cl.on_message` exigem o servidor
Chainlit rodando e dependem de I/O assíncrono — são exercitados
manualmente (`chainlit run app.py -w`).
"""

from __future__ import annotations

import json
import re
import uuid
from pathlib import Path

import app as app_module
import server
from app import (
    ACAO_IDENTIFICADO_TELEFONE,
    ACAO_NAO_IDENTIFICADO,
    COMANDO_ENCERRAR,
    COMANDO_TROCAR_TELEFONE,
    abreviar_nome,
    dividir_bolhas,
    exige_transferencia,
    extrair_acao_escolhida,
    extrair_texto_resposta_ask,
    formatar_classificacao,
    formatar_fontes,
    formatar_painel,
    mascarar_telefone,
    montar_chat_request,
    montar_contato_dev,
    montar_contato_telefone,
    normalizar_telefone,
    saudacao_dev,
)
from server import ChatRequest
from src.classificacao.taxonomia import carregar_taxonomia

# ---------------------------------------------------------------------------
# montar_contato_dev / normalizar_telefone / saudacao_dev
# ---------------------------------------------------------------------------


def test_normalizar_telefone_mantem_so_digitos():
    assert normalizar_telefone("(31) 99999-0001") == "31999990001"
    assert normalizar_telefone("+55 31 99999-0001") == "5531999990001"
    assert normalizar_telefone(None) == ""
    assert normalizar_telefone("abc") == ""


def test_montar_contato_dev_segue_o_contrato_a_sem_cache_de_identificacao():
    dados = montar_contato_dev("(31) 99999-0001", " Maria ")
    assert dados == {"telefone": "31999990001", "nome_contato": "Maria", "contato": None}


def test_montar_contato_dev_sem_nome():
    assert montar_contato_dev("31999990001", "")["nome_contato"] is None
    assert montar_contato_dev("31999990001", None)["nome_contato"] is None


def test_saudacao_dev_traz_nome_e_comando_de_troca():
    out = saudacao_dev("Maria")
    assert "**Maria**" in out
    assert "Sofia" in out
    assert COMANDO_TROCAR_TELEFONE in out


def test_saudacao_dev_aceita_input_invalido_sem_quebrar():
    assert "cliente" in saudacao_dev(None)
    assert "cliente" in saudacao_dev("   ")


# ---------------------------------------------------------------------------
# extrair_texto_resposta_ask
# ---------------------------------------------------------------------------


def test_extrair_texto_de_dict_com_chave_output():
    assert extrair_texto_resposta_ask({"output": "  31999990001  "}) == "31999990001"


def test_extrair_texto_de_dict_com_chave_content_alternativa():
    assert extrair_texto_resposta_ask({"content": "abc"}) == "abc"


def test_extrair_texto_de_objeto_com_output():
    class FakeMsg:
        output = "  zxy  "

    assert extrair_texto_resposta_ask(FakeMsg()) == "zxy"


def test_extrair_texto_aceita_none_dict_vazio_e_object_sem_campos():
    class Vazio:
        pass

    assert extrair_texto_resposta_ask(None) == ""
    assert extrair_texto_resposta_ask({}) == ""
    assert extrair_texto_resposta_ask(Vazio()) == ""


# ---------------------------------------------------------------------------
# formatar_fontes
# ---------------------------------------------------------------------------


def test_formatar_fontes_devolve_none_para_lista_vazia_ou_none():
    assert formatar_fontes([]) is None
    assert formatar_fontes(None) is None


def test_formatar_fontes_renderiza_url_como_link_clicavel():
    out = formatar_fontes(["https://exemplo.com.br/x"])
    assert "[https://exemplo.com.br/x](https://exemplo.com.br/x)" in out
    assert "🌐" in out


def test_formatar_fontes_renderiza_arquivo_em_backticks():
    out = formatar_fontes(["internet-residencial.md — Reiniciar o roteador"])
    assert "`internet-residencial.md — Reiniciar o roteador`" in out
    assert "📄" in out


def test_formatar_fontes_mix_url_e_arquivo_separados_por_meio_ponto():
    out = formatar_fontes(["a.md — X", "https://exemplo.com.br/x", "b.md — Y"])
    assert " · " in out
    assert out.count("📄") == 2
    assert out.count("🌐") == 1
    assert out.startswith("**Fontes consultadas:**")


# ---------------------------------------------------------------------------
# Sanidade do módulo
# ---------------------------------------------------------------------------


def test_singleton_do_grafo_e_lazy():
    """O grafo só é compilado na primeira chamada — `_GRAPH` começa None."""
    assert hasattr(app_module, "_GRAPH")
    assert callable(app_module._get_graph)


def test_comando_trocar_telefone_constante_existe():
    assert COMANDO_TROCAR_TELEFONE.startswith("/")
    assert "telefone" in COMANDO_TROCAR_TELEFONE


# ---------------------------------------------------------------------------
# Contrato A — pedido montado a partir das entradas do Chainlit
# (valida contra contratos/contrato-a/chat-request.json; sem rede)
# ---------------------------------------------------------------------------

RAIZ_REPO = Path(__file__).resolve().parents[2]
CONTRATO_A = RAIZ_REPO / "contratos" / "contrato-a"
MOCK_DADOS = RAIZ_REPO / "mock-infodesk" / "dados.json"
TAXONOMIA_DEV = RAIZ_REPO / "scripts" / "dev" / "taxonomia.dev.yaml"
TAXONOMIA_PLACEHOLDER = RAIZ_REPO / "agente-ia" / "config" / "taxonomia.yaml"

_FORMAS = {"telefone", "cpf", "contrato"}
_E164 = re.compile(r"^\+\d{8,15}$")
_UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")


def _json(nome: str) -> dict:
    return json.loads((CONTRATO_A / nome).read_text(encoding="utf-8"))


def _validar_chat_request(pedido: dict) -> None:
    """Confere `pedido` contra o exemplo `chat-request.json`: nenhuma chave fora
    do contrato, as obrigatórias presentes, mesmos tipos e `contato` nulo ou
    com as 4 chaves do exemplo. (O contrato é um exemplo, não um JSON Schema.)"""
    exemplo = _json("chat-request.json")
    assert set(pedido) <= set(exemplo), set(pedido) - set(exemplo)
    obrigatorias = {
        "conversation_id",
        "canal",
        "mensagem",
        "telefone",
        "contato",
        "estado_conversa",
    }
    assert obrigatorias <= set(pedido)
    for chave in ("conversation_id", "canal", "mensagem", "telefone", "estado_conversa"):
        assert isinstance(pedido[chave], type(exemplo[chave])), chave
    if "nome_contato" in pedido:
        assert isinstance(pedido["nome_contato"], str) and pedido["nome_contato"]
    assert _UUID.match(pedido["conversation_id"])
    assert pedido["canal"] == "whatsapp"
    assert pedido["estado_conversa"] == "bot"
    assert _E164.match(pedido["telefone"])
    contato = pedido["contato"]
    if contato is not None:
        assert set(contato) == set(exemplo["contato"])
        assert contato["forma"] in _FORMAS
        assert isinstance(contato["valor_mascarado"], str)
        assert contato["cliente_id"] is None or isinstance(contato["cliente_id"], str)
        assert contato["nome_cadastro"] is None or isinstance(contato["nome_cadastro"], str)


def test_pedido_sem_contato_identificado_valida_no_contrato_a():
    dados = montar_contato_dev("(55) 31 99000-0100", "Maria Souza")
    pedido = montar_chat_request(
        conversation_id=str(uuid.uuid4()),
        mensagem="minha internet caiu",
        telefone=dados["telefone"],
        nome_contato=dados["nome_contato"],
        contato=dados["contato"],
    )
    _validar_chat_request(pedido)
    assert pedido["contato"] is None
    assert pedido["telefone"] == "+5531990000100"
    assert pedido["nome_contato"] == "Maria Souza"
    # O ChatRequest do server.py aceita exatamente o que o harness monta.
    req = ChatRequest(**pedido)
    assert req.contato is None and req.estado_conversa == "bot"


def test_pedido_com_contato_identificado_pelo_telefone_valida_no_contrato_a():
    dados = montar_contato_dev("5531990000100", "Maria Souza", identificado=True)
    pedido = montar_chat_request(
        conversation_id=str(uuid.uuid4()),
        mensagem="quero saber do meu chamado",
        telefone=dados["telefone"],
        nome_contato=dados["nome_contato"],
        contato=dados["contato"],
    )
    _validar_chat_request(pedido)
    assert pedido["telefone"] == "+5531990000100"
    # A forma que o lookup do gateway manda: nome abreviado como valor_mascarado.
    assert pedido["contato"] == {
        "forma": "telefone",
        "valor_mascarado": "Maria S.",
        "cliente_id": None,
        "nome_cadastro": "Maria Souza",
    }
    assert ChatRequest(**pedido).contato["forma"] == "telefone"


def test_pedido_sem_nome_omite_nome_contato_como_o_gateway():
    pedido = montar_chat_request(
        conversation_id=str(uuid.uuid4()), mensagem="oi", telefone="5531990000100"
    )
    _validar_chat_request(pedido)
    assert "nome_contato" not in pedido


def test_contato_telefone_de_nome_unico_ou_vazio():
    assert abreviar_nome("Maria") == "Maria"
    assert abreviar_nome("  ") == ""
    assert abreviar_nome("Maria de Souza") == "Maria S."
    assert montar_contato_telefone("")["nome_cadastro"] is None


def test_pedido_nao_altera_o_contato_da_sessao():
    contato = montar_contato_telefone("Maria Souza")
    pedido = montar_chat_request(
        conversation_id=str(uuid.uuid4()), mensagem="oi", telefone="5531990000100", contato=contato
    )
    pedido["contato"]["forma"] = "cpf"
    assert contato["forma"] == "telefone"


def test_saudacao_dev_diz_se_o_contato_chega_identificado():
    assert "contato = null" in saudacao_dev("Maria")
    assert "identificado" in saudacao_dev("Maria", identificado=True)
    assert COMANDO_ENCERRAR in saudacao_dev("Maria")


# ---------------------------------------------------------------------------
# Telefone mascarado na tela
# ---------------------------------------------------------------------------


def test_mascarar_telefone_so_deixa_os_quatro_ultimos_digitos():
    assert mascarar_telefone("+55 (31) 99000-0100") == "****0100"
    assert mascarar_telefone("123") == "****"
    assert mascarar_telefone(None) == "****"


def test_painel_nunca_mostra_o_telefone_completo():
    resposta = _json("chat-response.json")
    painel = formatar_painel(
        resposta,
        telefone="+5531990000100",
        conversation_id="6f1c2a3e-9b4d-4e7a-8c21-5d3f0a9b7e10",
        contato=montar_contato_telefone("Maria Souza"),
    )
    assert "5531990000100" not in painel
    assert "31990000100" not in painel
    assert "****0100" in painel


# ---------------------------------------------------------------------------
# Contrato A — resposta renderizada a partir de chat-response.json
# ---------------------------------------------------------------------------


def test_painel_mostra_classificacao_e_acoes_do_contrato():
    resposta = _json("chat-response.json")
    painel = formatar_painel(resposta, telefone="+5531999990001", conversation_id="c-1")
    for chave, valor in resposta["classificacao"].items():
        assert f"| {chave} | {valor} |" in painel
    acao = resposta["acoes"][0]
    assert f"`{acao['tipo']}`" in painel
    assert f"origem `{acao['origem']}`" in painel
    assert acao["motivo"] in painel
    assert "erro:" not in painel


def test_painel_com_classificacao_e_acoes_vazias():
    painel = formatar_painel(
        {"reply": "oi", "acoes": [], "classificacao": None, "fontes": [], "erro": None},
        telefone="+5531999990001",
        conversation_id="c-1",
    )
    assert "**classificacao:** `null`" in painel
    assert "**acoes:** `[]`" in painel


def test_painel_mostra_o_envelope_de_erro():
    resposta = _json("chat-response-erro.json")
    painel = formatar_painel(resposta, telefone="+5531999990001", conversation_id="c-1")
    erro = resposta["erro"]
    assert erro["classe"] in painel
    assert erro["id_ocorrencia"] in painel
    assert erro["causa"] in painel


def test_painel_inclui_as_fontes_do_turno():
    painel = formatar_painel(
        {"fontes": ["internet-residencial.md — Reiniciar o roteador"]},
        telefone="+5531999990001",
        conversation_id="c-1",
    )
    assert "internet-residencial.md" in painel


def test_painel_mostra_campo_novo_da_classificacao_sem_mudar_o_harness():
    painel = formatar_classificacao({"categoria": "cobranca", "novo": "x"})
    assert "| novo | x |" in painel
    assert "| prioridade | — |" in painel


def test_exige_transferencia_so_com_acao_transferir():
    assert exige_transferencia(_json("chat-response.json")) is True
    assert exige_transferencia({"acoes": []}) is False
    assert exige_transferencia(None) is False


def test_dividir_bolhas_separa_no_marcador_do_contrato():
    reply = _json("chat-response.json")["reply"]
    bolhas = dividir_bolhas(reply)
    assert len(bolhas) == 2
    assert bolhas[1].startswith("Vou te passar para um atendente")
    assert all("---" not in b for b in bolhas)
    assert dividir_bolhas("") == []
    assert dividir_bolhas("sem divisor") == ["sem divisor"]


def test_resposta_do_contrato_cabe_no_chat_response_do_server():
    resposta = server.ChatResponse(**_json("chat-response.json"))
    painel = formatar_painel(resposta.model_dump(), telefone="+5531999990001", conversation_id="c")
    assert "sem_conexao" in painel


def test_extrair_acao_escolhida():
    assert (
        extrair_acao_escolhida({"name": ACAO_IDENTIFICADO_TELEFONE}) == ACAO_IDENTIFICADO_TELEFONE
    )
    assert extrair_acao_escolhida(None) == ""

    class Acao:
        name = ACAO_NAO_IDENTIFICADO

    assert extrair_acao_escolhida(Acao()) == ACAO_NAO_IDENTIFICADO


# ---------------------------------------------------------------------------
# Taxonomia de dev × mock-infodesk/dados.json
# ---------------------------------------------------------------------------


def _nomes(dados: dict, chave: str) -> set[str]:
    return {item["name"] for item in dados[chave]}


def test_taxonomia_dev_so_cita_mesas_e_departamentos_do_mock():
    dados = json.loads(MOCK_DADOS.read_text(encoding="utf-8"))
    mesas, departamentos = _nomes(dados, "desks"), _nomes(dados, "departments")
    tax = carregar_taxonomia(TAXONOMIA_DEV)

    citadas_mesas = {c.mesa for c in tax.categorias.values()} | {tax.mesa_triagem}
    citados_depts = {c.departamento for c in tax.categorias.values()} | {
        tax.departamento_humano_padrao
    }
    assert citadas_mesas <= mesas, citadas_mesas - mesas
    assert citados_depts <= departamentos, citados_depts - departamentos

    # Nomes de prioridade existem em TODA mesa citada (o gateway resolve por mesa).
    por_nome = {d["name"]: d for d in dados["desks"]}
    nomes_prio = {v for v in tax.prioridades.values() if v}
    assert nomes_prio, "o arquivo de dev deve declarar as prioridades por nome"
    for mesa in citadas_mesas:
        assert nomes_prio <= {p["name"] for p in por_nome[mesa]["priorities"]}, mesa


def test_taxonomia_dev_tem_as_mesmas_categorias_da_placeholder():
    dev = carregar_taxonomia(TAXONOMIA_DEV)
    placeholder = carregar_taxonomia(TAXONOMIA_PLACEHOLDER)
    assert set(dev.categorias) == set(placeholder.categorias)
    for nome, cat in dev.categorias.items():
        base = placeholder.categorias[nome]
        assert cat.descricao == base.descricao, nome
        assert cat.exemplos == base.exemplos, nome
        assert cat.nao_e == base.nao_e, nome
        assert cat.prioridade_minima == base.prioridade_minima, nome


def test_taxonomia_dev_diz_no_topo_que_nao_e_o_mapeamento_real():
    primeira = TAXONOMIA_DEV.read_text(encoding="utf-8").splitlines()[0]
    assert "SÓ PARA DEV" in primeira
    assert "P1" in primeira
    assert carregar_taxonomia(TAXONOMIA_DEV).placeholder is True
