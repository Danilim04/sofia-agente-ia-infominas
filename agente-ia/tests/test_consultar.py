"""`python -m src.rag.consultar`: formatação do top-k (sem rede, sem índice)."""

from langchain_core.documents import Document

from src.rag.consultar import formatar


def test_formatar_lista_fontes_secoes_e_trechos_curtos():
    docs = [
        Document(page_content="Texto   com\nquebras " + "x" * 300,
                 metadata={"source": "Azapfy Motorista (aplicativo)", "secao": "Login › Senha"}),
        Document(page_content="Sem seção.", metadata={"source": "Glossário"}),
    ]
    saida = formatar("como faço login?", docs, chars=40)
    assert saida.startswith("2 trecho(s) para 'como faço login?'")
    assert "[1] Azapfy Motorista (aplicativo) › Login › Senha" in saida
    assert "[2] Glossário" in saida and "›" not in saida.splitlines()[3]
    trecho1 = saida.splitlines()[2].strip()
    assert trecho1.startswith("Texto com quebras") and trecho1.endswith("…") and len(trecho1) == 40


def test_formatar_sem_resultados_avisa_indice_vazio():
    assert "índice vazio" in formatar("qualquer coisa", [])
