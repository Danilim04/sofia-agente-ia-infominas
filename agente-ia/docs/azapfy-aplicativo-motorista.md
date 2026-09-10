# Funcionalidades dos Aplicativos Azapfy

Neste documento, estão detalhadas as funções disponíveis no ecossistema de aplicativos da Azapfy, com foco no **Azapfy Motorista** e ferramentas de volumetria.

A Azapfy disponibiliza seus aplicativos para as plataformas Android e iOS (com maior maturidade atual na Play Store), atendendo desde gestores até equipes de conferência e, principalmente, motoristas responsáveis pelas comprovações de entrega.

---

## 1. Premissas da Plataforma

O aplicativo foi desenhado com base em três pilares fundamentais:
* **Velocidade:** Comprovações realizadas entre **3 e 8 segundos**, dependendo do aparelho.
* **Simplicidade:** O processo padrão de comprovação exige apenas **3 cliques**.
* **Segurança:** Sistema exclusivo de validação em tempo real. O aplicativo faz a leitura (OCR) da imagem e recusa a foto se o número da nota fiscal impressa não corresponder ao documento selecionado no sistema.

---

## 2. Acesso e Login

O acesso ao aplicativo é restrito a motoristas previamente cadastrados na plataforma Web da Azapfy.

* **Credenciais Padrão:** O **CPF** do motorista atua tanto como usuário (login) quanto como senha inicial.
* **Alteração de Senha:** No primeiro acesso, o sistema sugere a alteração da senha. Caso o motorista clique fora da janela, o CPF continuará sendo a senha ativa.
* **Recuperação de Senha:** Pode ser feita via botão "Esqueci minha senha" (caso o motorista tenha cadastrado um e-mail previamente) ou abrindo um chamado para a equipe de suporte realizar o *reset*.

---

## 3. Tela Inicial (Visão Geral)

A Tela Inicial organiza as entregas da jornada e mostra o progresso do motorista através de uma barra de percentual de documentos comprovados (caixa alaranjada).

Os documentos são classificados em três abas de **Status**:
* **Pendentes:** Notas abertas, sem nenhuma comprovação ou foto registrada, ou notas que sofreram uma ocorrência intermediária (ex: canhoto retido) e aguardam conclusão.
* **Entregues:** Notas que já foram comprovadas e fotografadas no aplicativo, mas que **ainda não foram sincronizadas** com a nuvem (geralmente por falta de sinal de internet).
* **Comprovadas:** Entregas concluídas e já sincronizadas com o banco de dados, estando disponíveis para consulta em tempo real na plataforma Web.

### 3.1. Agrupamento de Entregas
Para facilitar a busca pelas notas na Tela Inicial, o motorista pode visualizar as entregas em quatro visões diferentes:
* **Romaneios:** Agrupa os documentos pelo número e data de emissão do romaneio (viagem).
* **Destinatários:** Agrupa as notas pelo CNPJ/Nome do cliente final que receberá a carga.
* **Remetentes:** Agrupa as notas pelo embarcador/emissor do documento.
* **Rotas:** Agrupa todas as notas destinadas a uma mesma rota logística (acessível arrastando o menu lateralmente).

---

## 4. Menu Inferior (Funções Extras)

Na base do aplicativo, há duas ferramentas principais para navegação e atalhos operacionais:

### Bipar
* Aciona o leitor de código de barras ou QR Code pela câmera do celular.
* O motorista pode ler a chave de acesso de uma NFe ou CTe.
* Após a leitura, o app permite vincular aquele documento a um romaneio existente ou criar um novo. Em seguida, já abre a câmera para iniciar a comprovação.

### Perfil
* **Gestão de Conta:** Permite alterar a foto de perfil e confirmar dados cadastrais (Nome e CPF).
* **Gamificação:** Exibe as conquistas do motorista baseadas em seu histórico de entregas.
* **Contexto Operacional:** Permite selecionar em qual empresa ou base operacional ele está atuando no momento.
* **Suporte:** Opção para emissão de relatórios de uso e log de erros para ajudar o suporte técnico.
* **Sair (Logout):** Encerra a sessão. *Nota: Para não precisar digitar CPF e senha novamente no próximo uso, a recomendação é apenas fechar ou minimizar o aplicativo, sem clicar em "Sair".*

---

## 5. A Função "Comprovar" (Passo a Passo)

A comprovação é a principal função do aplicativo e foi desenhada para ser executada de forma muito rápida. 

**Fluxo de Entrega Normal (3 Cliques):**
1. Escolha o documento na aba **Pendentes** e clique sobre ele (a câmera abrirá automaticamente).
2. **Fotografe o canhoto**, garantindo que as informações estejam nítidas e legíveis.
3. Clique em **"Baixar documento"**. O sistema assumirá automaticamente que a entrega foi realizada com sucesso (Entrega Normal) e a sincronização ocorrerá em cerca de 2 segundos.

### 5.1. Registro de Ocorrências
Em casos onde a entrega não ocorre perfeitamente (cerca de 10% dos casos), o motorista deve registrar uma ocorrência. O passo a passo é o mesmo, mas **antes** de clicar em "Baixar documento", o motorista deve clicar em **"Ocorrência"** e selecionar uma das opções:

* **Devolução:** O destinatário rejeitou a entrega no ato.
* **Estabelecimento Fechado:** Não havia ninguém no local para receber a carga.
* **Avaria:** A entrega foi recusada por danos visíveis ao produto.
* **Extravio:** A mercadoria não estava fisicamente no caminhão no momento da entrega.
* **Canhoto Retido:** O cliente exigiu reter a nota para conferência posterior. O aplicativo "congela" a data e hora originais. Quando o motorista retornar para buscar o canhoto assinado e fotografá-lo, o sistema manterá o horário do registro inicial, garantindo o SLA da entrega.

### 5.2. Regra de Fotografias para Ocorrências
* **A 1ª Foto é sempre o documento:** Mesmo em casos de devolução, avaria ou estabelecimento fechado (sem assinatura), a primeira foto tirada deve ser do canhoto/NFe.
* **Evidências Fotográficas (Até 15 fotos):** O aplicativo permite anexar mais fotos na mesma ocorrência para comprovar o ocorrido (ex: foto das portas fechadas do comércio, foto do produto avariado ou do documento de justificativa de devolução).
