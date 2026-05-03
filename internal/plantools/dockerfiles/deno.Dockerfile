# keywords: typescript javascript
# description: Deno app with deps cache, deno-alpine runtime
FROM denoland/deno:alpine-2.1.0
WORKDIR /app
COPY --chown=deno:deno . .
USER deno
RUN --mount=type=cache,target=/deno-dir/deps,uid=1000 \
    deno cache --lock=deno.lock main.ts
EXPOSE 8000
CMD ["deno", "run", "--allow-net", "--allow-env", "main.ts"]
