/* eslint-disable comma-dangle, no-unused-vars, no-var, prefer-template, vars-on-top */
var subdomain = "<!--# echo var="subdomain" default="" -->";
if (subdomain) {
    subdomain = subdomain.substr(0,subdomain.length-1).split('.').join('_').toLowerCase() + '.';
}
var subdir = "<!--# echo var="subdir" default="" -->";
if (subdir.startsWith('<!--')) {
    subdir = '';
}
var config = {
    hosts: {
        // XMPP domain.
        domain: 'meet.example.org',

        // When using authentication, domain for guest users.
        anonymousdomain: 'guest.meet.example.org',

        // focus: 'focus.meet.example.org',

        // XMPP MUC domain. FIXME: use XEP-0030 to discover it.
        muc: 'conference.' + subdomain + 'meet.example.org'
    },

    // BOSH URL. FIXME: use XEP-0156 to discover it.
    bosh: '//meet.example.org/' + subdir + 'http-bind',

    // Websocket URL (XMPP RFC 7395 framing)
    websocket: 'wss://meet.example.org/' + subdir + 'xmpp-websocket', // comment
    focusUserJid: 'focus@auth.meet.example.org',
};
