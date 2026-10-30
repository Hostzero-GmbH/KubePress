<?php
/**
 * Plugin Name: KubePress Admin SSO
 * Description: Logs in a WordPress user presented with a short-lived token signed with the site's SSO key. Installed and managed by the KubePress operator; changes to this file are overwritten on the next pod start.
 * Version: 1.0.0
 *
 * Token format: base64url(payload JSON) "." base64url(HMAC-SHA256(payload, key))
 * Payload fields:
 *   email  (required) user to log in, matched against the WordPress user e-mail
 *   exp    (required) unix timestamp after which the token is rejected
 *   nonce  (required) random string, accepted once
 *   login  (optional) preferred user_login if the user has to be created
 *   name   (optional) display name if the user has to be created
 *   create (optional, default true) create the user as administrator if missing
 *   redirect (optional) wp-admin relative path to land on, e.g. "edit.php"
 *
 * The key comes from the KUBEPRESS_SSO_KEY environment variable, which the
 * operator sets from the site's Secret. The plugin only acts on HTTPS GET
 * requests carrying the kubepress_sso query parameter.
 */

if (!defined('ABSPATH')) {
    exit;
}

// Query parameter carrying the token.
define('KUBEPRESS_SSO_PARAM', 'kubepress_sso');
// Longest token lifetime accepted, regardless of what the issuer claims.
define('KUBEPRESS_SSO_MAX_TTL', 300);

/**
 * Handle a login token if the request carries one. Hooked early on init so
 * that no theme or plugin output has started yet.
 */
function kubepress_sso_maybe_login()
{
    if (empty($_GET[KUBEPRESS_SSO_PARAM])) {
        return;
    }
    if ((defined('WP_CLI') && WP_CLI) || wp_doing_cron() || wp_doing_ajax()) {
        return;
    }
    if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'GET') {
        return;
    }
    if (!is_ssl()) {
        kubepress_sso_fail('insecure transport');
    }

    $key = getenv('KUBEPRESS_SSO_KEY');
    if (!is_string($key) || strlen($key) < 32) {
        kubepress_sso_fail('key not configured');
    }

    $token = (string) wp_unslash($_GET[KUBEPRESS_SSO_PARAM]);
    $payload = kubepress_sso_verify($token, $key);
    if ($payload === null) {
        kubepress_sso_fail('invalid token');
    }

    $user = kubepress_sso_resolve_user($payload);
    if (!$user instanceof WP_User) {
        kubepress_sso_fail('unknown user');
    }

    wp_set_current_user($user->ID, $user->user_login);
    wp_set_auth_cookie($user->ID, false, true);
    do_action('wp_login', $user->user_login, $user);

    $target = admin_url();
    if (!empty($payload['redirect']) && is_string($payload['redirect'])) {
        $target = admin_url(ltrim($payload['redirect'], '/'));
    }
    wp_safe_redirect($target);
    exit;
}
add_action('init', 'kubepress_sso_maybe_login', 1);

/**
 * Verify the token signature, expiry and nonce. Returns the decoded payload
 * or null.
 */
function kubepress_sso_verify($token, $key)
{
    $parts = explode('.', $token);
    if (count($parts) !== 2) {
        return null;
    }
    list($payloadB64, $sigB64) = $parts;

    $payloadRaw = kubepress_sso_b64url_decode($payloadB64);
    $sig = kubepress_sso_b64url_decode($sigB64);
    if ($payloadRaw === false || $sig === false) {
        return null;
    }

    $expected = hash_hmac('sha256', $payloadRaw, $key, true);
    if (!hash_equals($expected, $sig)) {
        return null;
    }

    $payload = json_decode($payloadRaw, true);
    if (!is_array($payload)) {
        return null;
    }

    $now = time();
    if (empty($payload['exp']) || !is_numeric($payload['exp'])) {
        return null;
    }
    $exp = (int) $payload['exp'];
    if ($exp < $now || $exp > $now + KUBEPRESS_SSO_MAX_TTL) {
        return null;
    }

    if (empty($payload['nonce']) || !is_string($payload['nonce']) || strlen($payload['nonce']) < 8) {
        return null;
    }
    if (empty($payload['email']) || !is_email($payload['email'])) {
        return null;
    }

    // Each nonce is accepted once. The transient outlives the token so a
    // replay after expiry is rejected by the expiry check instead.
    $nonceKey = 'kubepress_sso_' . substr(hash('sha256', $payload['nonce']), 0, 32);
    if (get_transient($nonceKey) !== false) {
        return null;
    }
    set_transient($nonceKey, 1, $exp - $now + 60);

    return $payload;
}

/**
 * Find the user by e-mail, creating an administrator if allowed.
 */
function kubepress_sso_resolve_user(array $payload)
{
    $email = sanitize_email($payload['email']);
    $user = get_user_by('email', $email);
    if ($user instanceof WP_User) {
        return $user;
    }

    if (isset($payload['create']) && $payload['create'] === false) {
        return null;
    }

    $login = '';
    if (!empty($payload['login']) && is_string($payload['login'])) {
        $login = sanitize_user($payload['login'], true);
    }
    if ($login === '') {
        $login = sanitize_user(strstr($email, '@', true), true);
    }
    if ($login === '') {
        $login = 'user';
    }
    $base = $login;
    $i = 2;
    while (username_exists($login)) {
        $login = $base . $i++;
    }

    $name = '';
    if (!empty($payload['name']) && is_string($payload['name'])) {
        $name = sanitize_text_field($payload['name']);
    }

    $userId = wp_insert_user(array(
        'user_login'   => $login,
        'user_email'   => $email,
        'user_pass'    => wp_generate_password(32, true, true),
        'display_name' => $name !== '' ? $name : $login,
        'role'         => 'administrator',
    ));
    if (is_wp_error($userId)) {
        error_log('kubepress-sso: failed to create user: ' . $userId->get_error_message());
        return null;
    }

    return get_user_by('id', $userId);
}

function kubepress_sso_fail($reason)
{
    error_log('kubepress-sso: login rejected (' . $reason . ') from ' . ($_SERVER['REMOTE_ADDR'] ?? '?'));
    // Fall through to the normal page without the parameter so the token
    // does not stay in the address bar.
    wp_safe_redirect(remove_query_arg(KUBEPRESS_SSO_PARAM));
    exit;
}

function kubepress_sso_b64url_decode($s)
{
    if (!is_string($s) || $s === '' || preg_match('/[^A-Za-z0-9_-]/', $s)) {
        return false;
    }
    $pad = strlen($s) % 4;
    if ($pad) {
        $s .= str_repeat('=', 4 - $pad);
    }
    return base64_decode(strtr($s, '-_', '+/'), true);
}
