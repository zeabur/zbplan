# zbplan

A Dockerfile planner based on agents.

Hu et al. (2025) [^1] 已經試過了這個技巧：

- 先做出一個共同的 base，讓 agents 可以在上面做依賴安裝的實驗
- 依賴安裝完成後，跑 unit tests 確保可以正常執行
- 可以正常執行的話，生成一個 Dockerfile 可以用來編譯。

而 Zeabur 打算基於這個做出改進：

1. 不做出共同 base，而是透過 Registry API + fuzzy search 檢索版本 (e.g. `ubuntu:24.04`)。如果不確定的話，則給他一串版本列表讓 AI 選擇。
2. Zeabur 主要面向 Web Services，因此改成透過檢查特定的 port 是否可以連通，來決定是否採納這個 Dockerfile。
3. 採用 cache mount 來防止重新安裝依賴的開銷，但同時允許 Agent 直接修改整個 Dockerfile。

[^1]: Hu, R., Peng, C., Wang, X., Xu, J., & Gao, C. (2025). Repo2Run: Automated building executable environment for code repository at scale. arXiv. https://doi.org/10.48550/arXiv.2502.13681
