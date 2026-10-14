//go:build !darwin

package config

// DefaultDatapath — режим профиля без поля datapath: весь трафик системы
// через свой адаптер (решение пользователя 2026-10-14: клиенту нужен туннель
// для всего, а не прокси, который ещё надо настраивать в программах). Права
// администратора (root) qwdtt-cli при подключении из меню просит сам; консольный
// qwdtt-cli connect без них отказывает с подсказкой про -datapath socks.
const DefaultDatapath = DatapathTun
