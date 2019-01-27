package main

import (
	"fmt"
	"log"
	"strings"
	"time"
	"whapp-irc/ircConnection"

	"gopkg.in/sorcix/irc.v2"
	"gopkg.in/sorcix/irc.v2/ctcp"
)

func (conn *Connection) handleIRCCommand(msg *irc.Message) error {
	write := conn.irc.WriteNow
	status := conn.irc.Status

	switch msg.Command {
	case "PRIVMSG":
		to := msg.Params[0]

		body := msg.Params[1]
		if tag, text, ok := ctcp.Decode(msg.Trailing()); ok && tag == ctcp.ACTION {
			body = fmt.Sprintf("_%s_", text)
		}

		ircConnection.LogMessage(time.Now(), conn.irc.Nick(), to, body)

		if to == "status" {
			return nil
		}

		chat := conn.GetChatByIdentifier(to)
		if chat == nil {
			return status("unknown chat")
		}

		if err := conn.bridge.WI.SendMessageToChatID(
			conn.bridge.ctx,
			chat.ID,
			body,
		); err != nil {
			str := fmt.Sprintf("err while sending: %s", err.Error())
			log.Println(str)
			return status(str)
		}

	case "JOIN":
		idents := strings.Split(msg.Params[0], ",")
		for _, ident := range idents {
			chat := conn.GetChatByIdentifier(ident)
			if chat == nil {
				return status("chat not found: " + msg.Params[0])
			}

			if err := conn.joinChat(chat); err != nil {
				return status("error while joining: " + err.Error())
			}
		}

	case "PART":
		idents := strings.Split(msg.Params[0], ",")
		for _, ident := range idents {
			chat := conn.GetChatByIdentifier(ident)
			if chat == nil {
				return status("unknown chat")
			}

			// TODO: some way that we don't rejoin a person later.
			chat.Joined = false
		}

	case "MODE":
		if len(msg.Params) != 3 {
			return nil
		}

		ident := msg.Params[0]
		mode := msg.Params[1]
		nick := strings.ToLower(msg.Params[2])

		chat := conn.GetChatByIdentifier(ident)
		if chat == nil {
			return status("chat not found")
		}

		var op bool
		switch mode {
		case "+o":
			op = true
		case "-o":
			op = false

		default:
			return nil
		}

		for _, p := range chat.Participants {
			if strings.ToLower(p.SafeName()) != nick {
				continue
			}

			if err := chat.rawChat.SetAdmin(
				conn.bridge.ctx,
				conn.bridge.WI,
				p.ID,
				op,
			); err != nil {
				str := fmt.Sprintf("error while opping %s: %s", nick, err.Error())
				log.Println(str)
				return status(str)
			}

			return write(fmt.Sprintf(":%s MODE %s +o %s", conn.irc.Nick(), ident, nick))
		}

	case "LIST":
		// TODO: support args
		for _, c := range conn.chats {
			nParticipants := len(c.Participants)
			if !c.IsGroupChat {
				nParticipants = 2
			}

			str := fmt.Sprintf(
				":whapp-irc 322 %s %s %d :%s",
				conn.irc.Nick(),
				c.Identifier(),
				nParticipants,
				c.Name,
			)
			write(str)
		}
		write(fmt.Sprintf(":whapp-irc 323 %s :End of LIST", conn.irc.Nick()))

	case "WHO":
		identifier := msg.Params[0]
		chat := conn.GetChatByIdentifier(identifier)
		if chat != nil && chat.IsGroupChat {
			for _, p := range chat.Participants {
				if p.Contact.IsMe {
					continue
				}

				presenceStamp := "H"
				presence, found, err := conn.getPresenceByUserID(p.ID)
				if found && err == nil && !presence.IsOnline {
					presenceStamp = "G"
				}

				msg := fmt.Sprintf(
					":whapp-irc 352 %s %s %s whapp-irc whapp-irc %s %s :0 %s",
					conn.irc.Nick(),
					identifier,
					p.SafeName(),
					p.SafeName(),
					presenceStamp,
					p.FullName(),
				)
				if err := write(msg); err != nil {
					return err
				}
			}
		}
		write(fmt.Sprintf(":whapp-irc 315 %s %s :End of /WHO list.", conn.irc.Nick(), identifier))

	case "WHOIS": // TODO: fix
		chat := conn.GetChatByIdentifier(msg.Params[0])
		if chat == nil || chat.IsGroupChat {
			return write(fmt.Sprintf(":whapp-irc 401 %s %s :No such nick/channel", conn.irc.Nick(), msg.Params[0]))
		}
		identifier := chat.Identifier()

		str := fmt.Sprintf(
			":whapp-irc 311 %s %s ~%s whapp-irc * :%s",
			conn.irc.Nick(),
			identifier,
			identifier,
			chat.Name,
		)
		write(str)

		if groups, err := chat.rawChat.Contact.GetCommonGroups(
			conn.bridge.ctx,
			conn.bridge.WI,
		); err == nil && len(groups) > 0 {
			var names []string

			for _, group := range groups {
				// TODO: this could be more efficient: currently calling
				// `convertChat` makes it retrieve all participants in the
				// group, which is obviously not necessary.
				chat, err := conn.convertChat(group)
				if err != nil {
					continue
				}

				names = append(names, chat.Identifier())
			}

			str := fmt.Sprintf(
				":whapp-irc 319 %s %s :%s",
				conn.irc.Nick(),
				identifier,
				strings.Join(names, " "),
			)
			write(str)
		}

		write(fmt.Sprintf(":whapp-irc 318 %s %s :End of /WHOIS list.", conn.irc.Nick(), identifier))

	case "KICK":
		chatIdentifier := msg.Params[0]
		nick := strings.ToLower(msg.Params[1])

		chat := conn.GetChatByIdentifier(chatIdentifier)
		if chat == nil || !chat.IsGroupChat {
			str := fmt.Sprintf(
				":whapp-irc 403 %s %s :No such channel",
				conn.irc.Nick(),
				chatIdentifier,
			)
			return write(str)
		}

		for _, p := range chat.Participants {
			if strings.ToLower(p.SafeName()) != nick {
				continue
			}

			if err := chat.rawChat.RemoveParticipant(
				conn.bridge.ctx,
				conn.bridge.WI,
				p.ID,
			); err != nil {
				str := fmt.Sprintf("error while kicking %s: %s", nick, err.Error())
				log.Println(str)
				return status(str)
			}

			return nil
		}

	case "INVITE":
		nick := msg.Params[0]
		chatIdentifier := msg.Params[1]

		chat := conn.GetChatByIdentifier(chatIdentifier)
		if chat == nil || !chat.IsGroupChat {
			str := fmt.Sprintf(
				":whapp-irc 442 %s %s :You're not on that channel",
				conn.irc.Nick(),
				chatIdentifier,
			)
			return write(str)
		}
		personChat := conn.GetChatByIdentifier(nick)
		if personChat == nil || personChat.IsGroupChat {
			str := fmt.Sprintf(
				":whapp-irc 401 %s %s :No such nick/channel",
				conn.irc.Nick(),
				nick,
			)
			return write(str)
		}

		if err := chat.rawChat.AddParticipant(
			conn.bridge.ctx,
			conn.bridge.WI,
			personChat.ID,
		); err != nil {
			str := fmt.Sprintf("error while adding %s: %s", nick, err.Error())
			log.Println(str)
			return status(str)
		}
	}

	return nil
}
