# keywords: rails bundler sinatra gem
# description: Ruby single-stage with Bundler cache mount, ruby-alpine runtime
FROM ruby:3.3-alpine
WORKDIR /app
RUN --mount=type=cache,target=/usr/local/bundle \
    --mount=type=bind,source=Gemfile,target=Gemfile \
    --mount=type=bind,source=Gemfile.lock,target=Gemfile.lock \
    bundle install
COPY . .
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 3000
CMD ["ruby", "app.rb"]
