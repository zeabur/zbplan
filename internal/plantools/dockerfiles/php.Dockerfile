# keywords: composer laravel symfony
# description: PHP multi-stage: composer vendor install, php-fpm-alpine runtime
FROM composer:2 AS vendor
WORKDIR /app
COPY composer.json composer.lock ./
RUN --mount=type=cache,target=/root/.composer \
    composer install --no-dev --optimize-autoloader --no-interaction

FROM php:8.3-fpm-alpine
WORKDIR /app
COPY --from=vendor /app/vendor ./vendor
COPY . .
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 9000
CMD ["php-fpm"]
